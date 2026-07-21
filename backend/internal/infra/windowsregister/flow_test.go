package windowsregister

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type queuedBrowser struct {
	mu    sync.Mutex
	pages []BrowserPage
}

func (b *queuedBrowser) NewPage(context.Context) (BrowserPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	page := b.pages[0]
	b.pages = b.pages[1:]
	return page, nil
}
func (b *queuedBrowser) Close() error { return nil }

type fakeEmailProvider struct {
	mailbox Mailbox
	code    string
}

func (p fakeEmailProvider) Create(context.Context) (Mailbox, error)           { return p.mailbox, nil }
func (p fakeEmailProvider) PollCode(context.Context, Mailbox) (string, error) { return p.code, nil }

type memoryResultStore struct {
	mu      sync.Mutex
	records []Record
}

func (s *memoryResultStore) Append(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}
func (s *memoryResultStore) Read() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...), nil
}

func TestDiscoverSignupFromBrowserPage(t *testing.T) {
	html := readDiscoveryFixture(t, "signup_page.html")
	asset, _ := json.Marshal(`createUser(); const action="0123456789abcdef0123456789abcdef01234567";`)
	page := &scriptedBrowserPage{html: html, results: []json.RawMessage{asset}}
	cfg, err := discoverSignupFromPage(t.Context(), page)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActionID != "0123456789abcdef0123456789abcdef01234567" || len(page.navigated) != 1 {
		t.Fatalf("config=%#v navigated=%v", cfg, page.navigated)
	}
}

func TestNativeChallengeFlowProducesToken(t *testing.T) {
	page := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`"token"`)}}
	flow := nativeChallengeFlow{browser: &queuedBrowser{pages: []BrowserPage{page}}, config: SignupConfig{SiteKey: "0x4AAAAAAAtest"}, solver: TurnstileSolver{PollInterval: time.Millisecond, HardTimeout: time.Second}}
	token, err := flow.Produce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token.Value != "token" {
		t.Fatalf("token=%#v", token)
	}
}

func TestNativeMailFlowCreatesSendsAndPolls(t *testing.T) {
	page := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`{"status":200,"grpcStatus":"0","text":""}`)}}
	mailbox := Mailbox{Address: "u@x.test", Password: "password"}
	flow := nativeMailFlow{browser: &queuedBrowser{pages: []BrowserPage{page}}, provider: fakeEmailProvider{mailbox: mailbox, code: "123456"}}
	verified, err := flow.Produce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if verified.Mailbox.Address != mailbox.Address || verified.Code != "123456" || !page.closed {
		t.Fatalf("verified=%#v closed=%v", verified, page.closed)
	}
}

func TestNativeAccountFlowPersistsRegistration(t *testing.T) {
	page := &scriptedBrowserPage{
		results: []json.RawMessage{
			json.RawMessage(`{"status":200,"grpcStatus":"0","text":""}`),
			json.RawMessage(`{"status":200,"text":"https://accounts.x.ai/set-cookie?q=opaque"}`),
		},
		cookies: []BrowserCookie{{Name: "sso", Value: "session-token"}},
	}
	store := &memoryResultStore{}
	flow := nativeAccountFlow{browser: &queuedBrowser{pages: []BrowserPage{page}}, config: SignupConfig{ActionID: "action", StateTree: "state"}, store: store}
	record, err := flow.Consume(t.Context(), ChallengeToken{Value: "turnstile"}, VerifiedMailbox{Mailbox: Mailbox{Address: "u@x.test", Password: "password"}, Code: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	if record.SSO != "session-token" || len(store.records) != 1 || !page.closed {
		t.Fatalf("record=%#v stored=%#v closed=%v", record, store.records, page.closed)
	}
}
