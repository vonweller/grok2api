package windowsregister

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type scriptedBrowserPage struct {
	expressions []string
	arguments   [][]any
	results     []json.RawMessage
	cookies     []BrowserCookie
	navigated   []string
	closed      bool
	clicks      int
}

func (p *scriptedBrowserPage) Navigate(_ context.Context, rawURL string) error {
	p.navigated = append(p.navigated, rawURL)
	return nil
}

func (p *scriptedBrowserPage) HTML(context.Context) (string, error) { return "", nil }

func (p *scriptedBrowserPage) Evaluate(_ context.Context, expression string, args ...any) (json.RawMessage, error) {
	p.expressions = append(p.expressions, expression)
	p.arguments = append(p.arguments, args)
	if len(p.results) == 0 {
		return json.RawMessage(`null`), nil
	}
	result := p.results[0]
	p.results = p.results[1:]
	return result, nil
}

func (p *scriptedBrowserPage) Cookies(context.Context) ([]BrowserCookie, error) {
	return append([]BrowserCookie(nil), p.cookies...), nil
}

func (p *scriptedBrowserPage) Close() error { p.closed = true; return nil }

func (p *scriptedBrowserPage) Click(context.Context, float64, float64) error {
	p.clicks++
	return nil
}

func TestSignupSendCodeUsesBrowserContextWithoutInterpolation(t *testing.T) {
	page := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`{"status":200,"grpcStatus":"0","text":""}`)}}
	client := NewSignupClient(page, SignupConfig{})
	email := `sensitive@example.test`
	if err := client.SendCode(t.Context(), email); err != nil {
		t.Fatal(err)
	}
	if len(page.expressions) != 1 || !strings.Contains(page.expressions[0], "fetch(args.url") {
		t.Fatalf("expression = %q", page.expressions)
	}
	if strings.Contains(page.expressions[0], email) {
		t.Fatal("email was interpolated into JavaScript")
	}
}

func TestSignupVerifyCodeClassifiesRateLimit(t *testing.T) {
	page := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`{"status":429,"retryAfter":"15","grpcStatus":"8","text":""}`)}}
	client := NewSignupClient(page, SignupConfig{})
	err := client.VerifyCode(t.Context(), "sensitive@example.test", "123456")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v", err)
	}
}

func TestSignupRegisterReturnsAuthenticationCookie(t *testing.T) {
	page := &scriptedBrowserPage{
		results: []json.RawMessage{json.RawMessage(`{"status":200,"text":"https://accounts.x.ai/set-cookie?q=opaque-token"}`)},
		cookies: []BrowserCookie{{Name: "sso", Value: "session-token"}},
	}
	client := NewSignupClient(page, SignupConfig{ActionID: strings.Repeat("a", 40), StateTree: "state"})
	sso, err := client.Register(t.Context(), "user@example.test", "secret-password", "123456", "turnstile-token")
	if err != nil {
		t.Fatal(err)
	}
	if sso != "session-token" || len(page.navigated) != 1 {
		t.Fatalf("sso = %q navigated = %v", sso, page.navigated)
	}
	for _, secret := range []string{"user@example.test", "secret-password", "123456", "turnstile-token"} {
		if strings.Contains(page.expressions[0], secret) {
			t.Fatalf("secret %q was interpolated into JavaScript", secret)
		}
	}
}

func TestSignupRegisterRejectsUnsafeRedirectAndMissingCookie(t *testing.T) {
	unsafe := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`{"status":200,"text":"https://evil.example/set-cookie?q=opaque"}`)}}
	client := NewSignupClient(unsafe, SignupConfig{ActionID: strings.Repeat("a", 40), StateTree: "state"})
	if _, err := client.Register(t.Context(), "u@x.test", "password", "123456", "token"); !errors.Is(err, ErrSignupRedirect) {
		t.Fatalf("unsafe redirect error = %v", err)
	}

	missing := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`{"status":200,"text":"https://x.ai/set-cookie?q=opaque"}`)}}
	client = NewSignupClient(missing, SignupConfig{ActionID: strings.Repeat("a", 40), StateTree: "state"})
	if _, err := client.Register(t.Context(), "u@x.test", "password", "123456", "token"); !errors.Is(err, ErrAuthenticationCookie) {
		t.Fatalf("missing cookie error = %v", err)
	}
}

func TestSignupRegisterClassifiesChallengeAndDenial(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		want     error
	}{
		{name: "challenge", response: `{"status":403,"text":"cf-chl challenge-platform"}`, want: ErrChallengeRequired},
		{name: "denial", response: `{"status":400,"text":"account denied"}`, want: ErrRegistrationRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(test.response)}}
			client := NewSignupClient(page, SignupConfig{ActionID: strings.Repeat("a", 40), StateTree: "state"})
			_, err := client.Register(t.Context(), "u@x.test", "password", "123456", "token")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "u@x.test") {
				t.Fatalf("error leaked email: %v", err)
			}
		})
	}
}
