package windowsregister

import (
	"context"
	"errors"
)

var (
	ErrBrowserCrashed = errors.New("registration browser crashed")
	ErrEnginePanic    = errors.New("registration engine panic")
	ErrEngineConfig   = errors.New("registration engine configuration invalid")
)

type nativeFlowFunc func(context.Context, Browser, EmailProvider, ResultStore, RunObserver, StartOptions) error

type EngineDependencies struct {
	Browsers       BrowserFactory
	BrowserOptions BrowserLaunchOptions
	Emails         func(StartOptions) (EmailProvider, error)
	Store          ResultStore
	Observer       RunObserver
	flow           nativeFlowFunc
}

type Engine struct {
	deps EngineDependencies
}

func NewEngine(dependencies EngineDependencies) *Engine {
	if dependencies.Observer == nil {
		dependencies.Observer = noopRunObserver{}
	}
	if dependencies.flow == nil {
		dependencies.flow = runNativeFlow
	}
	return &Engine{deps: dependencies}
}

func (e *Engine) Run(ctx context.Context, options StartOptions) (runErr error) {
	if e == nil || e.deps.Browsers == nil || e.deps.Emails == nil || e.deps.Store == nil || e.deps.flow == nil {
		return ErrEngineConfig
	}
	provider, err := e.deps.Emails(options)
	if err != nil {
		return err
	}
	var active Browser
	defer func() {
		if active != nil {
			_ = active.Close()
		}
		if recover() != nil {
			runErr = ErrEnginePanic
		}
	}()
	for attempt := 0; attempt < 2; attempt++ {
		launchOptions := e.deps.BrowserOptions
		launchOptions.Proxy = options.Proxy
		active, err = e.deps.Browsers.Launch(ctx, launchOptions)
		if err != nil {
			return err
		}
		err = e.deps.flow(ctx, active, provider, e.deps.Store, e.deps.Observer, options)
		closeErr := active.Close()
		active = nil
		if err == nil && closeErr == nil {
			return nil
		}
		if err == nil {
			err = ErrBrowserCrashed
		}
		if !errors.Is(err, ErrBrowserCrashed) || attempt == 1 || ctx.Err() != nil {
			return err
		}
	}
	return ErrBrowserCrashed
}
