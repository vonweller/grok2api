package windowsregister_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	windowsregisterapp "github.com/chenyme/grok2api/backend/internal/application/windowsregister"
	windowsregisterinfra "github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

type fakeWorker struct {
	tokens map[string][]string
	err    error
}

func (f *fakeWorker) Status() windowsregisterinfra.Status {
	return windowsregisterinfra.Status{PlatformSupported: true, Ready: true, State: windowsregisterinfra.StateIdle}
}
func (f *fakeWorker) Start(opts windowsregisterinfra.StartOptions) (windowsregisterinfra.Status, error) {
	return f.Status(), nil
}
func (f *fakeWorker) Stop(ctx context.Context) (windowsregisterinfra.Status, error) {
	return f.Status(), nil
}
func (f *fakeWorker) ImportTokens(scope string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.tokens[scope]...), nil
}

type fakeImporter struct {
	webCalls     int
	consoleCalls int
	webErr       error
	consoleErr   error
	lastPayload  string
}

func (f *fakeImporter) ImportWebCredentials(ctx context.Context, data []byte) (accountapp.ImportResult, error) {
	f.webCalls++
	f.lastPayload = string(data)
	if f.webErr != nil {
		return accountapp.ImportResult{}, f.webErr
	}
	return accountapp.ImportResult{Created: 2, Updated: 0, Skipped: 1}, nil
}

func (f *fakeImporter) ImportConsoleCredentials(ctx context.Context, data []byte) (accountapp.ImportResult, error) {
	f.consoleCalls++
	f.lastPayload = string(data)
	if f.consoleErr != nil {
		return accountapp.ImportResult{}, f.consoleErr
	}
	return accountapp.ImportResult{Created: 1, Updated: 1, Skipped: 0}, nil
}

func TestImportDefaultsToWebAndConsole(t *testing.T) {
	worker := &fakeWorker{tokens: map[string][]string{"current": {"sso-a", "sso-b"}}}
	importer := &fakeImporter{}
	svc := windowsregisterapp.NewService(worker, importer)

	result, err := svc.Import(context.Background(), windowsregisterapp.ImportRequest{Scope: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceCount != 2 || len(result.Results) != 2 {
		t.Fatalf("result=%+v", result)
	}
	if importer.webCalls != 1 || importer.consoleCalls != 1 {
		t.Fatalf("calls web=%d console=%d", importer.webCalls, importer.consoleCalls)
	}
	if !strings.Contains(importer.lastPayload, "sso-a") {
		t.Fatalf("payload=%q", importer.lastPayload)
	}
}

func TestImportEmptyTokens(t *testing.T) {
	worker := &fakeWorker{err: windowsregisterinfra.ErrNoImportableAccounts}
	svc := windowsregisterapp.NewService(worker, &fakeImporter{})
	_, err := svc.Import(context.Background(), windowsregisterapp.ImportRequest{Scope: "all"})
	if !errors.Is(err, windowsregisterinfra.ErrNoImportableAccounts) {
		t.Fatalf("got %v", err)
	}
}

func TestImportPartialProviderError(t *testing.T) {
	worker := &fakeWorker{tokens: map[string][]string{"all": {"sso"}}}
	importer := &fakeImporter{consoleErr: errors.New("boom")}
	svc := windowsregisterapp.NewService(worker, importer)
	result, err := svc.Import(context.Background(), windowsregisterapp.ImportRequest{
		Scope:        "all",
		Destinations: []string{"grok_web", "grok_console"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Results[0].Error != "" || result.Results[1].Error == "" {
		t.Fatalf("results=%+v", result.Results)
	}
}
