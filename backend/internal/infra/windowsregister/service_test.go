package windowsregister_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

type fakeRunner struct {
	started chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}

func (r *fakeRunner) Run(ctx context.Context, _ windowsregister.StartOptions, observer windowsregister.RunObserver) error {
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		if r.err == nil {
			observer.Success(windowsregister.Record{})
		}
		return r.err
	}
}

func readyService(t *testing.T) (*windowsregister.Service, *fakeRunner, string) {
	t.Helper()
	dir := t.TempDir()
	browser := filepath.Join(dir, "chrome.exe")
	if err := os.WriteFile(browser, []byte("browser"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{started: make(chan struct{}), release: make(chan struct{})}
	service := windowsregister.NewService(windowsregister.Config{Enabled: true, BrowserPath: browser, OutputDir: filepath.Join(dir, "out")})
	service.SetPlatformSupported(true)
	service.SetRunner(runner)
	return service, runner, dir
}

func TestServiceStartRunsNativeRunnerAndRejectsDuplicate(t *testing.T) {
	service, runner, _ := readyService(t)
	status, err := service.Start(windowsregister.StartOptions{Target: 1, EmailMode: "tempmail"})
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if !status.Running || status.State != windowsregister.StateRunning {
		t.Fatalf("status=%+v", status)
	}
	if _, err := service.Start(windowsregister.StartOptions{Target: 1}); !errors.Is(err, windowsregister.ErrAlreadyRunning) {
		t.Fatalf("duplicate error=%v", err)
	}
	close(runner.release)
	waitServiceState(t, service, windowsregister.StateCompleted)
	if service.Status().Success != 1 {
		t.Fatalf("status=%+v", service.Status())
	}
}

func TestServiceStopCancelsNativeRunner(t *testing.T) {
	service, runner, _ := readyService(t)
	if _, err := service.Start(windowsregister.StartOptions{Target: 2}); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	status, err := service.Stop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != windowsregister.StateStopped || status.Running {
		t.Fatalf("status=%+v", status)
	}
}

func TestServiceRunnerErrorAndPanicBecomeErrorState(t *testing.T) {
	for _, test := range []struct {
		name   string
		runner windowsregister.Runner
	}{
		{name: "error", runner: runnerFunc(func(context.Context, windowsregister.StartOptions, windowsregister.RunObserver) error {
			return errors.New("runner failed")
		})},
		{name: "panic", runner: runnerFunc(func(context.Context, windowsregister.StartOptions, windowsregister.RunObserver) error {
			panic("secret panic")
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := readyService(t)
			service.SetRunner(test.runner)
			if _, err := service.Start(windowsregister.StartOptions{Target: 1}); err != nil {
				t.Fatal(err)
			}
			status := waitServiceState(t, service, windowsregister.StateError)
			if status.LastError == "" || status.Running {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

type runnerFunc func(context.Context, windowsregister.StartOptions, windowsregister.RunObserver) error

func (f runnerFunc) Run(ctx context.Context, options windowsregister.StartOptions, observer windowsregister.RunObserver) error {
	return f(ctx, options, observer)
}

func TestServiceReadinessOnlyRequiresBrowser(t *testing.T) {
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LocalAppData", "")
	service := windowsregister.NewService(windowsregister.Config{Enabled: true, BrowserPath: filepath.Join(t.TempDir(), "missing.exe"), OutputDir: t.TempDir()})
	service.SetPlatformSupported(true)
	status := service.Status()
	if status.Ready || len(status.Missing) != 1 || status.Missing[0] != "browser" {
		t.Fatalf("status=%+v", status)
	}
}

func TestServiceImportTokensScope(t *testing.T) {
	service, runner, dir := readyService(t)
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, "accounts.txt")
	if err := os.WriteFile(path, []byte("a@x.test:p:sso1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(windowsregister.StartOptions{Target: 1}); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := os.WriteFile(path, []byte("a@x.test:p:sso1\nb@x.test:p:sso2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	waitServiceState(t, service, windowsregister.StateCompleted)
	tokens, err := service.ImportTokens("current")
	if err != nil || len(tokens) != 1 || tokens[0] != "sso2" {
		t.Fatalf("tokens=%v err=%v", tokens, err)
	}
	all, err := service.ImportTokens("all")
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%v err=%v", all, err)
	}
}

func TestServiceNonWindowsNotSupported(t *testing.T) {
	service := windowsregister.NewService(windowsregister.Config{Enabled: true, OutputDir: t.TempDir()})
	service.SetPlatformSupported(false)
	_, err := service.Start(windowsregister.StartOptions{Target: 1})
	if !errors.Is(err, windowsregister.ErrPlatformUnsupported) {
		t.Fatalf("error=%v", err)
	}
}

func waitServiceState(t *testing.T, service *windowsregister.Service, expected windowsregister.State) windowsregister.Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if status.State == expected {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state=%s, want %s", service.Status().State, expected)
	return windowsregister.Status{}
}
