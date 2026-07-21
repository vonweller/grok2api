package windowsregister

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCustomEmailProviderCreateAndPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/check/user@example.test" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "123456"})
	}))
	defer server.Close()

	provider := NewCustomEmailProvider(server.URL, "example.test", server.Client())
	provider.addressSource = func(string) (string, error) { return "user@example.test", nil }
	provider.pollInterval = time.Millisecond
	provider.pollTimeout = time.Second
	mailbox, err := provider.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Address != "user@example.test" || mailbox.Password == "" {
		t.Fatalf("mailbox = %#v", mailbox)
	}
	code, err := provider.PollCode(t.Context(), mailbox)
	if err != nil {
		t.Fatal(err)
	}
	if code != "123456" {
		t.Fatalf("code = %q", code)
	}
}

func TestExtractEmailCode(t *testing.T) {
	tests := map[string]string{
		`<b>ABC-123</b>`: "ABC123",
		`<b>ZX9Q8W</b>`:  "ZX9Q8W",
		`code: 987654`:   "987654",
		`no short code`:  "",
	}
	for input, want := range tests {
		if got := extractEmailCode(input); got != want {
			t.Fatalf("extractEmailCode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNewEmailProviderSelectsSupportedModes(t *testing.T) {
	if provider, err := NewEmailProvider("tempmail", "", "", nil); err != nil {
		t.Fatal(err)
	} else if _, ok := provider.(*TempMailProvider); !ok {
		t.Fatalf("tempmail provider = %T", provider)
	}
	if provider, err := NewEmailProvider("custom", "http://127.0.0.1:8080", "example.test", nil); err != nil {
		t.Fatal(err)
	} else if _, ok := provider.(*CustomEmailProvider); !ok {
		t.Fatalf("custom provider = %T", provider)
	}
	if _, err := NewEmailProvider("unknown", "", "", nil); !errors.Is(err, ErrEmailProviderUnavailable) {
		t.Fatalf("unknown mode error = %v", err)
	}
}

func TestCustomEmailProviderErrorsAreBoundedAndSanitized(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{name: "non-2xx", handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "raw-sensitive", http.StatusBadGateway) }, want: ErrEmailResponse},
		{name: "malformed JSON", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"code":`)) }, want: ErrEmailResponse},
		{name: "oversized", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxEmailResponseBytes+1)))
		}, want: ErrEmailResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()
			provider := NewCustomEmailProvider(server.URL, "example.test", server.Client())
			provider.pollInterval = time.Millisecond
			provider.pollTimeout = time.Second
			mailbox := Mailbox{Address: "user@example.test", Password: "secret-password"}
			_, err := provider.PollCode(t.Context(), mailbox)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			message := err.Error()
			for _, secret := range []string{mailbox.Address, mailbox.Password, "raw-sensitive"} {
				if strings.Contains(message, secret) {
					t.Fatalf("error leaked %q: %s", secret, message)
				}
			}
		})
	}
}

func TestEmailStatusResponseIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(strings.Repeat("x", maxEmailResponseBytes+1)))
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = checkEmailStatus(t.Context(), configuredHTTPClient(server.Client()), request)
	if !errors.Is(err, ErrEmailResponse) {
		t.Fatalf("error = %v, want ErrEmailResponse", err)
	}
}

func TestCustomEmailProviderDomainTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"code": ""})
	}))
	defer server.Close()
	provider := NewCustomEmailProvider(server.URL, "example.test", server.Client())
	provider.pollInterval = time.Millisecond
	provider.pollTimeout = 10 * time.Millisecond

	if _, err := provider.PollCode(t.Context(), Mailbox{Address: "user@wrong.test"}); !errors.Is(err, ErrEmailDomain) {
		t.Fatalf("domain error = %v", err)
	}
	if _, err := provider.PollCode(t.Context(), Mailbox{Address: "user@example.test"}); !errors.Is(err, ErrEmailTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := provider.PollCode(cancelled, Mailbox{Address: "user@example.test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestTempMailProviderCreateAndPollLOL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/inbox/create":
			_ = json.NewEncoder(w).Encode(map[string]string{"address": "user@temp.test", "token": "mail-token"})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/inbox" && r.URL.Query().Get("token") == "mail-token":
			_ = json.NewEncoder(w).Encode(map[string]any{"emails": []map[string]string{{"subject": "code", "body": ">ABC-123<"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewTempMailProvider(server.Client())
	provider.lolBase = server.URL
	provider.mailTMBase = nil
	provider.pollInterval = time.Millisecond
	provider.pollTimeout = time.Second
	mailbox, err := provider.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	code, err := provider.PollCode(t.Context(), mailbox)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Address != "user@temp.test" || code != "ABC123" {
		t.Fatalf("mailbox = %#v code = %q", mailbox, code)
	}
}

func TestTempMailProviderFallsBackToMailTM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lol/"):
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case r.Method == http.MethodGet && r.URL.Path == "/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"hydra:member": []map[string]any{{"domain": "mail.test", "isActive": true, "isPrivate": false}}})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tm-token"})
		case r.Method == http.MethodGet && r.URL.Path == "/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"hydra:member": []map[string]string{{"id": "message-1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/messages/message-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"subject": "verify", "text": "code 654321"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewTempMailProvider(server.Client())
	provider.lolBase = server.URL + "/lol"
	provider.mailTMBase = []string{server.URL}
	provider.pollInterval = time.Millisecond
	provider.pollTimeout = time.Second
	mailbox, err := provider.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	code, err := provider.PollCode(t.Context(), mailbox)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(mailbox.Address, "@mail.test") || code != "654321" {
		t.Fatalf("mailbox = %#v code = %q", mailbox, code)
	}
}
