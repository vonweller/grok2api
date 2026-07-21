package windowsregister

import (
	"context"
	"errors"
	"testing"
)

type fakeEngineBrowser struct{ closed bool }

func (b *fakeEngineBrowser) NewPage(context.Context) (BrowserPage, error) {
	return &scriptedBrowserPage{}, nil
}
func (b *fakeEngineBrowser) Close() error { b.closed = true; return nil }

type fakeEngineBrowserFactory struct {
	browsers []*fakeEngineBrowser
	launches int
}

func (f *fakeEngineBrowserFactory) Launch(context.Context, BrowserLaunchOptions) (Browser, error) {
	browser := &fakeEngineBrowser{}
	f.browsers = append(f.browsers, browser)
	f.launches++
	return browser, nil
}

func TestEngineLaunchesBrowserRunsFlowAndCloses(t *testing.T) {
	factory := &fakeEngineBrowserFactory{}
	observer := &recordingObserver{}
	engine := NewEngine(EngineDependencies{
		Browsers: factory,
		Emails:   func(StartOptions) (EmailProvider, error) { return nil, nil },
		Store:    NewFileResultStore(t.TempDir() + "/accounts.txt"),
		Observer: observer,
		flow: func(context.Context, Browser, EmailProvider, ResultStore, RunObserver, StartOptions) error {
			observer.Success(Record{Email: "u@x.test", Password: "p", SSO: "sso"})
			return nil
		},
	})
	if err := engine.Run(t.Context(), StartOptions{Target: 1, EmailMode: "tempmail"}); err != nil {
		t.Fatal(err)
	}
	if factory.launches != 1 || !factory.browsers[0].closed || observer.SuccessCount() != 1 {
		t.Fatalf("launches=%d closed=%v success=%d", factory.launches, factory.browsers[0].closed, observer.SuccessCount())
	}
}

func TestEngineRestartsBrowserOnce(t *testing.T) {
	factory := &fakeEngineBrowserFactory{}
	calls := 0
	engine := NewEngine(EngineDependencies{
		Browsers: factory,
		Emails:   func(StartOptions) (EmailProvider, error) { return nil, nil },
		Store:    NewFileResultStore(t.TempDir() + "/accounts.txt"),
		flow: func(context.Context, Browser, EmailProvider, ResultStore, RunObserver, StartOptions) error {
			calls++
			if calls == 1 {
				return ErrBrowserCrashed
			}
			return nil
		},
	})
	if err := engine.Run(t.Context(), StartOptions{Target: 1}); err != nil {
		t.Fatal(err)
	}
	if factory.launches != 2 || !factory.browsers[0].closed || !factory.browsers[1].closed {
		t.Fatalf("launches=%d browsers=%#v", factory.launches, factory.browsers)
	}
}

func TestEngineSecondBrowserCrashFails(t *testing.T) {
	factory := &fakeEngineBrowserFactory{}
	engine := NewEngine(EngineDependencies{
		Browsers: factory,
		Emails:   func(StartOptions) (EmailProvider, error) { return nil, nil },
		Store:    NewFileResultStore(t.TempDir() + "/accounts.txt"),
		flow: func(context.Context, Browser, EmailProvider, ResultStore, RunObserver, StartOptions) error {
			return ErrBrowserCrashed
		},
	})
	err := engine.Run(t.Context(), StartOptions{Target: 1})
	if !errors.Is(err, ErrBrowserCrashed) || factory.launches != 2 {
		t.Fatalf("error=%v launches=%d", err, factory.launches)
	}
}

func TestEngineRecoversPanicAndClosesBrowser(t *testing.T) {
	factory := &fakeEngineBrowserFactory{}
	engine := NewEngine(EngineDependencies{
		Browsers: factory,
		Emails:   func(StartOptions) (EmailProvider, error) { return nil, nil },
		Store:    NewFileResultStore(t.TempDir() + "/accounts.txt"),
		flow: func(context.Context, Browser, EmailProvider, ResultStore, RunObserver, StartOptions) error {
			panic("secret panic payload")
		},
	})
	err := engine.Run(t.Context(), StartOptions{Target: 1})
	if !errors.Is(err, ErrEnginePanic) || !factory.browsers[0].closed {
		t.Fatalf("error=%v closed=%v", err, factory.browsers[0].closed)
	}
}
