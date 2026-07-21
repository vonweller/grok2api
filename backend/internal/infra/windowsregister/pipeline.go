package windowsregister

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

var ErrPipelineOptions = errors.New("registration pipeline options invalid")

type ChallengeToken struct {
	Value string
}

type VerifiedMailbox struct {
	Mailbox Mailbox
	Code    string
}

type ChallengeProducer interface {
	Produce(context.Context) (ChallengeToken, error)
}

type MailProducer interface {
	Produce(context.Context) (VerifiedMailbox, error)
}

type AccountConsumer interface {
	Consume(context.Context, ChallengeToken, VerifiedMailbox) (Record, error)
}

type RunObserver interface {
	Success(Record)
	Failure(error)
	RateLimited(time.Duration)
	Log(string)
}

type Pipeline struct {
	Challenges ChallengeProducer
	Mail       MailProducer
	Accounts   AccountConsumer
	Observer   RunObserver
}

type PipelineOptions struct {
	Target            int
	SWorkers          int
	PWorkers          int
	CWorkers          int
	QueueSize         int
	RateLimitCooldown time.Duration
}

type noopRunObserver struct{}

func (noopRunObserver) Success(Record)            {}
func (noopRunObserver) Failure(error)             {}
func (noopRunObserver) RateLimited(time.Duration) {}
func (noopRunObserver) Log(string)                {}

type rateLimitCircuit struct {
	mu        sync.Mutex
	openUntil time.Time
	cooldown  time.Duration
	observer  RunObserver
}

func (p Pipeline) Run(parent context.Context, options PipelineOptions) error {
	options, err := normalizedPipelineOptions(options)
	if err != nil || p.Challenges == nil || p.Mail == nil || p.Accounts == nil {
		return ErrPipelineOptions
	}
	observer := p.Observer
	if observer == nil {
		observer = noopRunObserver{}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	challengeQueue := make(chan ChallengeToken, options.QueueSize)
	mailQueue := make(chan VerifiedMailbox, options.QueueSize)
	targetReached := make(chan struct{})
	var targetOnce sync.Once
	var successes atomic.Int64
	circuit := &rateLimitCircuit{cooldown: options.RateLimitCooldown, observer: observer}

	var challengeGroup errgroup.Group
	for range options.SWorkers {
		challengeGroup.Go(func() error {
			produceChallenges(ctx, p.Challenges, observer, challengeQueue)
			return nil
		})
	}
	challengeDone := make(chan struct{})
	go func() {
		_ = challengeGroup.Wait()
		close(challengeQueue)
		close(challengeDone)
	}()

	var mailGroup errgroup.Group
	for range options.PWorkers {
		mailGroup.Go(func() error {
			produceMail(ctx, p.Mail, observer, mailQueue)
			return nil
		})
	}
	mailDone := make(chan struct{})
	go func() {
		_ = mailGroup.Wait()
		close(mailQueue)
		close(mailDone)
	}()

	var accountGroup errgroup.Group
	for range options.CWorkers {
		accountGroup.Go(func() error {
			consumeAccounts(ctx, p.Accounts, observer, circuit, challengeQueue, mailQueue, options.Target, &successes, func() {
				targetOnce.Do(func() {
					close(targetReached)
					cancel()
				})
			})
			return nil
		})
	}
	accountDone := make(chan struct{})
	go func() {
		_ = accountGroup.Wait()
		close(accountDone)
	}()

	select {
	case <-targetReached:
	case <-parent.Done():
		cancel()
	case <-accountDone:
		cancel()
	}
	<-challengeDone
	<-mailDone
	<-accountDone
	if successes.Load() >= int64(options.Target) {
		return nil
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return ErrPipelineOptions
}

func normalizedPipelineOptions(options PipelineOptions) (PipelineOptions, error) {
	if options.Target < 1 || options.Target > 10000 {
		return PipelineOptions{}, ErrPipelineOptions
	}
	for _, workers := range []*int{&options.SWorkers, &options.PWorkers, &options.CWorkers} {
		if *workers == 0 {
			*workers = 1
		}
		if *workers < 1 || *workers > 64 {
			return PipelineOptions{}, ErrPipelineOptions
		}
	}
	if options.QueueSize == 0 {
		options.QueueSize = 4
	}
	if options.QueueSize < 1 || options.QueueSize > 1024 {
		return PipelineOptions{}, ErrPipelineOptions
	}
	if options.RateLimitCooldown <= 0 {
		options.RateLimitCooldown = time.Minute
	}
	if options.RateLimitCooldown > 24*time.Hour {
		return PipelineOptions{}, ErrPipelineOptions
	}
	return options, nil
}

func produceChallenges(ctx context.Context, producer ChallengeProducer, observer RunObserver, output chan<- ChallengeToken) {
	for {
		item, err := producer.Produce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			observer.Failure(err)
			if !waitForPipeline(ctx, 10*time.Millisecond) {
				return
			}
			continue
		}
		select {
		case output <- item:
		case <-ctx.Done():
			return
		}
	}
}

func produceMail(ctx context.Context, producer MailProducer, observer RunObserver, output chan<- VerifiedMailbox) {
	for {
		item, err := producer.Produce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			observer.Failure(err)
			if !waitForPipeline(ctx, 10*time.Millisecond) {
				return
			}
			continue
		}
		select {
		case output <- item:
		case <-ctx.Done():
			return
		}
	}
}

func consumeAccounts(ctx context.Context, consumer AccountConsumer, observer RunObserver, circuit *rateLimitCircuit, challenges <-chan ChallengeToken, mail <-chan VerifiedMailbox, target int, successes *atomic.Int64, reached func()) {
	for {
		challenge, ok := receiveChallenge(ctx, challenges)
		if !ok {
			return
		}
		mailbox, ok := receiveMailbox(ctx, mail)
		if !ok {
			return
		}
		for {
			if !circuit.Wait(ctx) {
				return
			}
			record, err := consumer.Consume(ctx, challenge, mailbox)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, ErrRateLimited) {
					circuit.Open(err)
					continue
				}
				observer.Failure(err)
				break
			}
			count := successes.Add(1)
			if count <= int64(target) {
				observer.Success(record)
			}
			if count >= int64(target) {
				reached()
			}
			break
		}
	}
}

func receiveChallenge(ctx context.Context, input <-chan ChallengeToken) (ChallengeToken, bool) {
	select {
	case item, ok := <-input:
		return item, ok
	case <-ctx.Done():
		return ChallengeToken{}, false
	}
}

func receiveMailbox(ctx context.Context, input <-chan VerifiedMailbox) (VerifiedMailbox, bool) {
	select {
	case item, ok := <-input:
		return item, ok
	case <-ctx.Done():
		return VerifiedMailbox{}, false
	}
}

func (c *rateLimitCircuit) Open(err error) {
	cooldown := c.cooldown
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) && rateLimit.RetryAfter > cooldown {
		cooldown = rateLimit.RetryAfter
	}
	now := time.Now()
	c.mu.Lock()
	emit := !now.Before(c.openUntil)
	until := now.Add(cooldown)
	if until.After(c.openUntil) {
		c.openUntil = until
	}
	c.mu.Unlock()
	if emit {
		c.observer.RateLimited(cooldown)
	}
}

func (c *rateLimitCircuit) Wait(ctx context.Context) bool {
	for {
		c.mu.Lock()
		remaining := time.Until(c.openUntil)
		c.mu.Unlock()
		if remaining <= 0 {
			return true
		}
		if !waitForPipeline(ctx, remaining) {
			return false
		}
	}
}

func waitForPipeline(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
