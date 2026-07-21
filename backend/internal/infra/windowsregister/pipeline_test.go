package windowsregister

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeChallenges struct{ next atomic.Int64 }

func (f *fakeChallenges) Produce(context.Context) (ChallengeToken, error) {
	return ChallengeToken{Value: fmt.Sprintf("challenge-%d", f.next.Add(1))}, nil
}

type fakeMail struct{ next atomic.Int64 }

func (f *fakeMail) Produce(context.Context) (VerifiedMailbox, error) {
	i := f.next.Add(1)
	return VerifiedMailbox{Mailbox: Mailbox{Address: fmt.Sprintf("u%d@x.test", i), Password: "password"}, Code: "123456"}, nil
}

type fakeAccounts struct {
	rateLimitOnce atomic.Bool
	calls         atomic.Int64
}

func (f *fakeAccounts) Consume(_ context.Context, _ ChallengeToken, mailbox VerifiedMailbox) (Record, error) {
	f.calls.Add(1)
	if f.rateLimitOnce.CompareAndSwap(true, false) {
		return Record{}, &RateLimitError{RetryAfter: time.Millisecond}
	}
	return Record{Email: mailbox.Mailbox.Address, Password: mailbox.Mailbox.Password, SSO: "sso-" + mailbox.Mailbox.Address}, nil
}

type recordingObserver struct {
	mu          sync.Mutex
	successes   []Record
	failures    int
	rateLimited int
}

func (o *recordingObserver) Success(record Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.successes = append(o.successes, record)
}

func (o *recordingObserver) Failure(error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures++
}

func (o *recordingObserver) RateLimited(time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rateLimited++
}

func (o *recordingObserver) Log(string) {}

func (o *recordingObserver) SuccessCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.successes)
}

func TestPipelineStopsAtTarget(t *testing.T) {
	observer := &recordingObserver{}
	pipeline := Pipeline{
		Challenges: &fakeChallenges{},
		Mail:       &fakeMail{},
		Accounts:   &fakeAccounts{},
		Observer:   observer,
	}
	err := pipeline.Run(t.Context(), PipelineOptions{Target: 3, SWorkers: 1, PWorkers: 1, CWorkers: 1, QueueSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if observer.SuccessCount() != 3 {
		t.Fatalf("success = %d", observer.SuccessCount())
	}
}

type blockingProducer struct{}

func (blockingProducer) Produce(ctx context.Context) (ChallengeToken, error) {
	<-ctx.Done()
	return ChallengeToken{}, ctx.Err()
}

type blockingMailProducer struct{}

func (blockingMailProducer) Produce(ctx context.Context) (VerifiedMailbox, error) {
	<-ctx.Done()
	return VerifiedMailbox{}, ctx.Err()
}

func TestPipelineCancellationDoesNotLeakWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	before := runtime.NumGoroutine()
	pipeline := Pipeline{Challenges: blockingProducer{}, Mail: blockingMailProducer{}, Accounts: &fakeAccounts{}}
	done := make(chan error, 1)
	go func() {
		done <- pipeline.Run(ctx, PipelineOptions{Target: 10, SWorkers: 2, PWorkers: 2, CWorkers: 2, QueueSize: 2})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pipeline did not stop")
	}
	runtime.Gosched()
	if after := runtime.NumGoroutine(); after > before+3 {
		t.Fatalf("worker leak: before=%d after=%d", before, after)
	}
}

func TestPipelineRateLimitOpensOneCircuitWindow(t *testing.T) {
	observer := &recordingObserver{}
	accounts := &fakeAccounts{}
	accounts.rateLimitOnce.Store(true)
	pipeline := Pipeline{Challenges: &fakeChallenges{}, Mail: &fakeMail{}, Accounts: accounts, Observer: observer}
	err := pipeline.Run(t.Context(), PipelineOptions{Target: 2, SWorkers: 1, PWorkers: 1, CWorkers: 2, QueueSize: 2, RateLimitCooldown: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if observer.rateLimited != 1 {
		t.Fatalf("rate limit events = %d", observer.rateLimited)
	}
	if accounts.calls.Load() < 3 {
		t.Fatalf("consume calls = %d", accounts.calls.Load())
	}
}

type alwaysRateLimitedAccounts struct{}

func (alwaysRateLimitedAccounts) Consume(context.Context, ChallengeToken, VerifiedMailbox) (Record, error) {
	return Record{}, &RateLimitError{RetryAfter: time.Hour}
}

type rateLimitSignalObserver struct {
	recordingObserver
	once sync.Once
	seen chan struct{}
}

func (o *rateLimitSignalObserver) RateLimited(duration time.Duration) {
	o.recordingObserver.RateLimited(duration)
	o.once.Do(func() { close(o.seen) })
}

func TestPipelineCancellationBypassesRateLimitCooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	observer := &rateLimitSignalObserver{seen: make(chan struct{})}
	pipeline := Pipeline{Challenges: &fakeChallenges{}, Mail: &fakeMail{}, Accounts: alwaysRateLimitedAccounts{}, Observer: observer}
	done := make(chan error, 1)
	go func() {
		done <- pipeline.Run(ctx, PipelineOptions{Target: 1, RateLimitCooldown: time.Hour})
	}()
	select {
	case <-observer.seen:
	case <-time.After(time.Second):
		t.Fatal("rate-limit circuit did not open")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cooldown ignored cancellation")
	}
}

type crashingChallengeProducer struct{}

func (crashingChallengeProducer) Produce(context.Context) (ChallengeToken, error) {
	return ChallengeToken{}, ErrBrowserCrashed
}

func TestPipelinePropagatesBrowserCrash(t *testing.T) {
	pipeline := Pipeline{Challenges: crashingChallengeProducer{}, Mail: &fakeMail{}, Accounts: &fakeAccounts{}}
	err := pipeline.Run(t.Context(), PipelineOptions{Target: 1})
	if !errors.Is(err, ErrBrowserCrashed) {
		t.Fatalf("error = %v, want ErrBrowserCrashed", err)
	}
}
