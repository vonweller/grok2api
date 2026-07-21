package windowsregister

import (
	"errors"
	"testing"

	"github.com/go-rod/rod/lib/launcher/flags"
)

func TestRodLauncherUsesConfiguredExecutableAndProxy(t *testing.T) {
	executable := touchBrowserFile(t, t.TempDir()+"/chrome.exe")
	l, err := newRodLauncher(BrowserLaunchOptions{
		ExecutablePath: executable,
		Proxy:          "http://127.0.0.1:7890",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Get(flags.Bin); got != executable {
		t.Fatalf("executable = %q, want %q", got, executable)
	}
	if !l.Has(flags.Headless) {
		t.Fatal("headless browser flag is missing")
	}
	if got := l.Get(flags.ProxyServer); got != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q", got)
	}
}

func TestRodLauncherRejectsMissingExecutable(t *testing.T) {
	_, err := newRodLauncher(BrowserLaunchOptions{ExecutablePath: "missing-browser.exe"})
	if !errors.Is(err, ErrBrowserUnavailable) {
		t.Fatalf("error = %v, want ErrBrowserUnavailable", err)
	}
}
