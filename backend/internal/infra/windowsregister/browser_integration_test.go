package windowsregister

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRodBrowserFixture(t *testing.T) {
	browserPath := os.Getenv("GROK2API_TEST_BROWSER")
	if browserPath == "" {
		t.Skip("GROK2API_TEST_BROWSER is not set")
	}

	server := fixtureServer(t)
	factory := NewRodBrowserFactory()
	browser, err := factory.Launch(t.Context(), BrowserLaunchOptions{ExecutablePath: browserPath})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	page, err := browser.NewPage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	if err := page.Navigate(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	raw, err := page.Evaluate(t.Context(), `() => document.title`)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"grok2api-register-fixture"` {
		t.Fatalf("title = %s", raw)
	}
	cookies, err := page.Cookies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0].Name != "fixture" || cookies[0].Value != "ready" {
		t.Fatalf("cookies = %#v", cookies)
	}
	centerRaw, err := page.Evaluate(t.Context(), `() => { const r = document.querySelector('#click-target').getBoundingClientRect(); return {x: r.left + r.width / 2, y: r.top + r.height / 2}; }`)
	if err != nil {
		t.Fatal(err)
	}
	var center struct{ X, Y float64 }
	if err := json.Unmarshal(centerRaw, &center); err != nil {
		t.Fatal(err)
	}
	clicker, ok := page.(browserPageClicker)
	if !ok {
		t.Fatal("browser page does not support coordinate clicks")
	}
	if err := clicker.Click(t.Context(), center.X, center.Y); err != nil {
		t.Fatal(err)
	}
	clicks, err := page.Evaluate(t.Context(), `() => window.fixtureClicks`)
	if err != nil || string(clicks) != "1" {
		t.Fatalf("click count = %s, error = %v", clicks, err)
	}
}

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	html, err := os.ReadFile(filepath.Join("testdata", "browser_fixture.html"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "fixture", Value: "ready", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	}))
	t.Cleanup(server.Close)
	return server
}
