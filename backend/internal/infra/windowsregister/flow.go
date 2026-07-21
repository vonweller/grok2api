package windowsregister

import (
	"context"
	"encoding/json"
	"runtime"
)

const signupAssetFetchScript = `(path) => fetch(path).then(async response => { const text = await response.text(); return text.slice(0, 8388609); })`

type nativeChallengeFlow struct {
	browser Browser
	config  SignupConfig
	solver  TurnstileSolver
}

type nativeMailFlow struct {
	browser  Browser
	provider EmailProvider
}

type nativeAccountFlow struct {
	browser Browser
	config  SignupConfig
	store   ResultStore
}

func runNativeFlow(ctx context.Context, browser Browser, provider EmailProvider, store ResultStore, observer RunObserver, options StartOptions) error {
	if browser == nil || provider == nil || store == nil {
		return ErrEngineConfig
	}
	discoveryPage, err := browser.NewPage(ctx)
	if err != nil {
		return ErrBrowserCrashed
	}
	config, err := discoverSignupFromPage(ctx, discoveryPage)
	_ = discoveryPage.Close()
	if err != nil {
		return err
	}
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}
	if workers > options.Target {
		workers = options.Target
	}
	pipeline := Pipeline{
		Challenges: nativeChallengeFlow{browser: browser, config: config, solver: TurnstileSolver{}},
		Mail:       nativeMailFlow{browser: browser, provider: provider},
		Accounts:   nativeAccountFlow{browser: browser, config: config, store: store},
		Observer:   observer,
	}
	return pipeline.Run(ctx, PipelineOptions{
		Target:    options.Target,
		SWorkers:  workers,
		PWorkers:  workers,
		CWorkers:  workers,
		QueueSize: workers * 2,
	})
}

func discoverSignupFromPage(ctx context.Context, page BrowserPage) (SignupConfig, error) {
	if err := page.Navigate(ctx, signupBaseURL+"/sign-up?redirect=grok-com"); err != nil {
		if ctx.Err() != nil {
			return SignupConfig{}, ctx.Err()
		}
		return SignupConfig{}, ErrBrowserCrashed
	}
	document, err := page.HTML(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return SignupConfig{}, ctx.Err()
		}
		return SignupConfig{}, ErrBrowserCrashed
	}
	assets := make(map[string]string)
	for _, match := range scriptSourcePattern.FindAllStringSubmatch(document, maxDiscoveryAssets) {
		if len(match) < 2 {
			continue
		}
		raw, evaluateErr := page.Evaluate(ctx, signupAssetFetchScript, match[1])
		if evaluateErr != nil {
			if ctx.Err() != nil {
				return SignupConfig{}, ctx.Err()
			}
			continue
		}
		var asset string
		if json.Unmarshal(raw, &asset) == nil && len(asset) <= maxDiscoveryAssetBytes {
			assets[match[1]] = asset
		}
	}
	return discoverSignupConfig(document, assets)
}

func (f nativeChallengeFlow) Produce(ctx context.Context) (ChallengeToken, error) {
	page, err := f.browser.NewPage(ctx)
	if err != nil {
		return ChallengeToken{}, ErrBrowserCrashed
	}
	if err := page.Navigate(ctx, signupBaseURL+"/sign-up"); err != nil {
		_ = page.Close()
		if ctx.Err() != nil {
			return ChallengeToken{}, ctx.Err()
		}
		return ChallengeToken{}, ErrBrowserCrashed
	}
	token, err := f.solver.Solve(ctx, page, f.config.SiteKey)
	if err != nil {
		return ChallengeToken{}, err
	}
	return ChallengeToken{Value: token}, nil
}

func (f nativeMailFlow) Produce(ctx context.Context) (VerifiedMailbox, error) {
	mailbox, err := f.provider.Create(ctx)
	if err != nil {
		return VerifiedMailbox{}, err
	}
	page, err := f.browser.NewPage(ctx)
	if err != nil {
		return VerifiedMailbox{}, ErrBrowserCrashed
	}
	defer page.Close()
	if err := page.Navigate(ctx, signupBaseURL+"/sign-up"); err != nil {
		if ctx.Err() != nil {
			return VerifiedMailbox{}, ctx.Err()
		}
		return VerifiedMailbox{}, ErrBrowserCrashed
	}
	client := NewSignupClient(page, SignupConfig{})
	if err := client.SendCode(ctx, mailbox.Address); err != nil {
		return VerifiedMailbox{}, err
	}
	code, err := f.provider.PollCode(ctx, mailbox)
	if err != nil {
		return VerifiedMailbox{}, err
	}
	return VerifiedMailbox{Mailbox: mailbox, Code: code}, nil
}

func (f nativeAccountFlow) Consume(ctx context.Context, challenge ChallengeToken, mailbox VerifiedMailbox) (Record, error) {
	page, err := f.browser.NewPage(ctx)
	if err != nil {
		return Record{}, ErrBrowserCrashed
	}
	defer page.Close()
	if err := page.Navigate(ctx, signupBaseURL+"/sign-up"); err != nil {
		if ctx.Err() != nil {
			return Record{}, ctx.Err()
		}
		return Record{}, ErrBrowserCrashed
	}
	client := NewSignupClient(page, f.config)
	if err := client.VerifyCode(ctx, mailbox.Mailbox.Address, mailbox.Code); err != nil {
		return Record{}, err
	}
	sso, err := client.Register(ctx, mailbox.Mailbox.Address, mailbox.Mailbox.Password, mailbox.Code, challenge.Value)
	if err != nil {
		return Record{}, err
	}
	record := Record{Email: mailbox.Mailbox.Address, Password: mailbox.Mailbox.Password, SSO: sso}
	if err := f.store.Append(record); err != nil {
		return Record{}, err
	}
	return record, nil
}
