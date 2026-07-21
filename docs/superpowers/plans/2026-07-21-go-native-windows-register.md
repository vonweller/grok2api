# Go Native Windows Registration Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Python/Playwright/CloakBrowser Windows registration worker with a native Go engine that controls an external Chromium browser through CDP while preserving the existing admin API and import workflow.

**Architecture:** Keep the application and HTTP contracts unchanged. Add focused native components under `internal/infra/windowsregister` for browser control, dynamic signup discovery, email, protocol encoding, registration flow, bounded pipeline, and result persistence; then replace the subprocess-backed service with the native engine. Remove Python packaging only after browser fixture and authorized smoke gates pass.

**Tech Stack:** Go 1.26, `github.com/go-rod/rod`, standard `net/http`, Gin existing APIs, PowerShell 5.1 packaging, React admin unchanged.

---

## File map

New backend files:

- `backend/internal/infra/windowsregister/browser.go`: browser/page ports and launch options.
- `backend/internal/infra/windowsregister/browser_rod.go`: Rod CDP adapter.
- `backend/internal/infra/windowsregister/browser_paths_windows.go`: Windows Chrome/Edge discovery.
- `backend/internal/infra/windowsregister/browser_paths_other.go`: non-Windows fallback for tests.
- `backend/internal/infra/windowsregister/protocol.go`: protobuf/gRPC-Web and JWT helpers.
- `backend/internal/infra/windowsregister/discovery.go`: Site Key, Action ID, and router state extraction.
- `backend/internal/infra/windowsregister/email.go`: email provider interface and factory.
- `backend/internal/infra/windowsregister/email_tempmail.go`: public temporary email implementation.
- `backend/internal/infra/windowsregister/email_custom.go`: custom webhook implementation.
- `backend/internal/infra/windowsregister/signup.go`: browser-context signup operations.
- `backend/internal/infra/windowsregister/turnstile.go`: challenge page lifecycle and token polling.
- `backend/internal/infra/windowsregister/pipeline.go`: bounded S/P/C pipeline.
- `backend/internal/infra/windowsregister/store.go`: durable compatible account output.
- `backend/internal/infra/windowsregister/engine.go`: native run orchestration.

New test files mirror each component with `_test.go`. The existing application and HTTP tests remain the contract regression suite.

Modified files:

- `backend/go.mod`, `backend/go.sum`: Rod dependency.
- `backend/internal/infra/windowsregister/service.go`: native engine lifecycle instead of subprocess.
- `backend/internal/infra/windowsregister/paths.go`: browser-only readiness.
- `backend/internal/infra/config/config.go`: add `browserPath`, accept deprecated Python fields.
- `backend/internal/app/application.go`: construct native dependencies.
- `config.example.yaml`: browser-based configuration.
- `scripts/windows/package.ps1`: stop copying Python source and assert it is absent.
- `scripts/windows/deploy.ps1`: remove Python/venv/PyPI setup and resolve/install browser only.
- `README.md`, `README.zh-CN.md`, `WINDOWS_DEPLOYMENT.md`: native runtime instructions.

Deleted after parity gate:

- `backend/internal/infra/windowsregister/process_windows.go`
- `backend/internal/infra/windowsregister/process_other.go`
- `tools/windows-register/**`

## Task 1: Native browser configuration and path resolution

**Files:**

- Modify: `backend/internal/infra/config/config.go`
- Modify: `backend/internal/infra/config/config_test.go`
- Modify: `backend/internal/infra/windowsregister/paths.go`
- Create: `backend/internal/infra/windowsregister/browser_paths_windows.go`
- Create: `backend/internal/infra/windowsregister/browser_paths_other.go`
- Test: `backend/internal/infra/windowsregister/browser_paths_test.go`

- [ ] **Step 1: Write failing config compatibility tests**

Add tests that load both the new field and a legacy config without rejecting old keys:

```go
func TestLoadWindowsRegisterBrowserPath(t *testing.T) {
    path := writeConfig(t, `
windowsRegister:
  enabled: true
  browserPath: ./runtime/chrome.exe
  outputDir: ./data/windows-register
`)
    cfg, err := Load(path)
    if err != nil { t.Fatal(err) }
    if !filepath.IsAbs(cfg.WindowsRegister.BrowserPath) {
        t.Fatalf("browserPath was not resolved: %q", cfg.WindowsRegister.BrowserPath)
    }
}

func TestLoadAcceptsDeprecatedWindowsRegisterFields(t *testing.T) {
    path := writeConfig(t, `
windowsRegister:
  enabled: true
  enginePath: ./tools/windows-register
  pythonPath: python
  outputDir: ./data/windows-register
`)
    if _, err := Load(path); err != nil {
        t.Fatalf("legacy config must remain loadable: %v", err)
    }
}
```

- [ ] **Step 2: Run the config tests and verify the new field fails**

Run:

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/config -run WindowsRegister -count=1
```

Expected: compile failure because `BrowserPath` is not defined.

- [ ] **Step 3: Add the new config field while retaining deprecated fields**

Use this exact structure and resolve `BrowserPath` relative to the config directory:

```go
type WindowsRegisterConfig struct {
    Enabled     bool   `yaml:"enabled"`
    BrowserPath string `yaml:"browserPath"`
    OutputDir   string `yaml:"outputDir"`
    // Deprecated compatibility fields. Native registration ignores them.
    EnginePath string `yaml:"enginePath"`
    PythonPath string `yaml:"pythonPath"`
}
```

Set defaults to `Enabled: true`, empty browser path, and `./data/windows-register`.

- [ ] **Step 4: Write failing browser candidate ordering tests**

```go
func TestResolveBrowserPathPrefersExplicitThenManagedThenSystem(t *testing.T) {
    root := t.TempDir()
    explicit := touchFile(t, root, "explicit.exe")
    managed := touchFile(t, root, "managed/chrome.exe")
    got := resolveBrowserPath(explicit, managed, []string{"system.exe"})
    if got != explicit { t.Fatalf("got %q", got) }

    got = resolveBrowserPath("", managed, []string{"system.exe"})
    if got != managed { t.Fatalf("got %q", got) }
}

func TestResolveBrowserPathReturnsEmptyWhenNoCandidateExists(t *testing.T) {
    if got := resolveBrowserPath("", "missing-managed.exe", []string{"missing.exe"}); got != "" {
        t.Fatalf("got %q", got)
    }
}
```

- [ ] **Step 5: Implement browser-only path resolution**

Replace Python/CloakBrowser probing with:

```go
func resolveBrowserPath(configured, managed string, system []string) string {
    candidates := append([]string{strings.TrimSpace(configured), strings.TrimSpace(managed)}, system...)
    for _, candidate := range candidates {
        candidate = strings.TrimSpace(candidate)
        if candidate != "" && fileExists(candidate) {
            absolute, err := filepath.Abs(candidate)
            if err == nil { return filepath.Clean(absolute) }
        }
    }
    return ""
}
```

On Windows, return common Chrome and Edge installation paths from `ProgramFiles`, `ProgramFiles(x86)`, and `LocalAppData`. Non-Windows returns `nil` so unit tests stay portable.

- [ ] **Step 6: Run tests and commit**

Run:

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/config ./internal/infra/windowsregister -count=1
```

Expected: PASS.

Commit:

```powershell
git add backend/internal/infra/config backend/internal/infra/windowsregister/paths.go backend/internal/infra/windowsregister/browser_paths_*.go
git commit -m "feat: configure native Windows registration browser"
```

## Task 2: Browser ports and Rod CDP adapter

**Files:**

- Modify: `backend/go.mod`
- Modify: `backend/go.sum`
- Create: `backend/internal/infra/windowsregister/browser.go`
- Create: `backend/internal/infra/windowsregister/browser_rod.go`
- Create: `backend/internal/infra/windowsregister/browser_test.go`
- Create: `backend/internal/infra/windowsregister/testdata/browser_fixture.html`
- Create: `backend/internal/infra/windowsregister/browser_integration_test.go`

- [ ] **Step 1: Define the browser contract in a failing test**

```go
func TestBrowserCookieSelection(t *testing.T) {
    cookies := []BrowserCookie{
        {Name: "other", Value: "x"},
        {Name: "sso", Value: "wanted", Domain: ".x.ai", Path: "/"},
    }
    value, ok := authenticationCookie(cookies)
    if !ok || value != "wanted" { t.Fatalf("value=%q ok=%v", value, ok) }
}
```

Expected browser interfaces:

```go
type BrowserFactory interface {
    Launch(ctx context.Context, options BrowserLaunchOptions) (Browser, error)
}

type Browser interface {
    NewPage(ctx context.Context) (BrowserPage, error)
    Close() error
}

type BrowserPage interface {
    Navigate(ctx context.Context, rawURL string) error
    HTML(ctx context.Context) (string, error)
    Evaluate(ctx context.Context, expression string, args ...any) (json.RawMessage, error)
    Cookies(ctx context.Context) ([]BrowserCookie, error)
    Close() error
}
```

- [ ] **Step 2: Run the test and verify it fails to compile**

Run:

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister -run BrowserCookieSelection -count=1
```

Expected: compile failure because `BrowserCookie` and `authenticationCookie` do not exist.

- [ ] **Step 3: Add Rod and implement the adapter**

Run:

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe get github.com/go-rod/rod@v0.116.2
```

Implement `rodBrowserFactory.Launch` with `launcher.New().Bin(options.ExecutablePath).Headless(true).Launch()`, connect with `rod.New().ControlURL(controlURL).Context(ctx).Connect()`, and wrap every Rod operation with the supplied context. Launch only the new browser process; never attach to a user's existing debugging port.

- [ ] **Step 4: Add an opt-in fixture integration test**

```go
func TestRodBrowserFixture(t *testing.T) {
    browserPath := os.Getenv("GROK2API_TEST_BROWSER")
    if browserPath == "" { t.Skip("GROK2API_TEST_BROWSER is not set") }
    factory := NewRodBrowserFactory()
    browser, err := factory.Launch(t.Context(), BrowserLaunchOptions{ExecutablePath: browserPath})
    if err != nil { t.Fatal(err) }
    defer browser.Close()
    page, err := browser.NewPage(t.Context())
    if err != nil { t.Fatal(err) }
    defer page.Close()
    if err := page.Navigate(t.Context(), fixtureServer(t).URL); err != nil { t.Fatal(err) }
    raw, err := page.Evaluate(t.Context(), `() => document.title`)
    if err != nil { t.Fatal(err) }
    if string(raw) != `"grok2api-register-fixture"` { t.Fatalf("raw=%s", raw) }
}
```

- [ ] **Step 5: Run unit tests, optional integration test, and commit**

Run unit tests first. If Chrome is installed, set `GROK2API_TEST_BROWSER` and run `-run RodBrowserFixture`.

Commit:

```powershell
git add backend/go.mod backend/go.sum backend/internal/infra/windowsregister/browser*
git commit -m "feat: add Chromium CDP adapter for registration"
```

## Task 3: Protocol and dynamic signup discovery

**Files:**

- Create: `backend/internal/infra/windowsregister/protocol.go`
- Create: `backend/internal/infra/windowsregister/protocol_test.go`
- Create: `backend/internal/infra/windowsregister/discovery.go`
- Create: `backend/internal/infra/windowsregister/discovery_test.go`
- Create: `backend/internal/infra/windowsregister/testdata/signup_page.html`

- [ ] **Step 1: Write failing protobuf frame tests**

```go
func TestCreateEmailValidationFrame(t *testing.T) {
    got := createEmailValidationFrame("a@b")
    want := []byte{0, 0, 0, 0, 5, 0x0a, 0x03, 'a', '@', 'b'}
    if !bytes.Equal(got, want) { t.Fatalf("got %x want %x", got, want) }
}

func TestVerifyEmailValidationFrame(t *testing.T) {
    got := verifyEmailValidationFrame("a@b", "123456")
    if got[0] != 0 || binary.BigEndian.Uint32(got[1:5]) != uint32(len(got)-5) {
        t.Fatalf("invalid grpc-web frame: %x", got)
    }
}
```

- [ ] **Step 2: Verify RED, then implement bounded varint/string encoding**

Use `encoding/binary`; reject strings larger than 1 MiB before allocating. Run `go test ... -run ValidationFrame` until PASS.

- [ ] **Step 3: Write failing discovery fixture tests**

```go
func TestDiscoverSignupConfig(t *testing.T) {
    html := readFixture(t, "signup_page.html")
    cfg, err := discoverSignupConfig(html, map[string]string{
        "/_next/static/app.js": `const action="0123456789abcdef0123456789abcdef01234567"; createUser();`,
    })
    if err != nil { t.Fatal(err) }
    if cfg.SiteKey != "0x4AAAAAAAtest_site_key" { t.Fatalf("site=%q", cfg.SiteKey) }
    if cfg.ActionID != "0123456789abcdef0123456789abcdef01234567" { t.Fatalf("action=%q", cfg.ActionID) }
    if cfg.StateTree == "" { t.Fatal("state tree empty") }
}
```

- [ ] **Step 4: Implement deterministic discovery**

Define:

```go
type SignupConfig struct {
    SiteKey  string
    ActionID string
    StateTree string
}
```

Limit HTML to 8 MiB, inspect at most 50 same-origin `/_next/static/*.js` assets, limit each JS response to 8 MiB, and return `ErrConfigDiscovery` unless all three fields are present.

- [ ] **Step 5: Run tests and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister -run "ValidationFrame|DiscoverSignup" -count=1
git add internal/infra/windowsregister/protocol* internal/infra/windowsregister/discovery* internal/infra/windowsregister/testdata/signup_page.html
git commit -m "feat: port registration protocol discovery to Go"
```

## Task 4: Email providers

**Files:**

- Create: `backend/internal/infra/windowsregister/email.go`
- Create: `backend/internal/infra/windowsregister/email_tempmail.go`
- Create: `backend/internal/infra/windowsregister/email_custom.go`
- Create: `backend/internal/infra/windowsregister/email_test.go`

- [ ] **Step 1: Write failing custom email tests with `httptest.Server`**

```go
func TestCustomEmailProviderCreateAndPoll(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/check/user@example.test": json.NewEncoder(w).Encode(map[string]string{"code":"123456"})
        default: http.NotFound(w, r)
        }
    }))
    defer server.Close()
    provider := NewCustomEmailProvider(server.URL, "example.test", server.Client())
    provider.addressSource = func(string) (string, error) { return "user@example.test", nil }
    mailbox, err := provider.Create(t.Context())
    if err != nil { t.Fatal(err) }
    code, err := provider.PollCode(t.Context(), mailbox)
    if err != nil { t.Fatal(err) }
    if code != "123456" { t.Fatalf("code=%q", code) }
}
```

- [ ] **Step 2: Verify RED and define the provider contract**

```go
type Mailbox struct {
    Address string
    Password string
    Handle json.RawMessage
}

type EmailProvider interface {
    Create(context.Context) (Mailbox, error)
    PollCode(context.Context, Mailbox) (string, error)
}
```

- [ ] **Step 3: Implement custom and temporary providers**

Use a shared `http.Client` with a 20-second transport timeout. Wrap response bodies in `http.MaxBytesReader` equivalent logic using `io.LimitReader(..., 1<<20)`. Poll every two seconds until 90 seconds or context cancellation. Accept only six-character numeric/alphanumeric codes using the exact extractor covered by tests.

- [ ] **Step 4: Add error tests**

Cover non-2xx, malformed JSON, oversized response, mismatched domain, empty code, timeout, and cancellation. Error strings must not contain mailbox passwords or raw response bodies.

- [ ] **Step 5: Run tests and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister -run Email -count=1
git add internal/infra/windowsregister/email*.go
git commit -m "feat: implement Go registration email providers"
```

## Task 5: Signup browser flow and Turnstile token lifecycle

**Files:**

- Create: `backend/internal/infra/windowsregister/signup.go`
- Create: `backend/internal/infra/windowsregister/signup_test.go`
- Create: `backend/internal/infra/windowsregister/turnstile.go`
- Create: `backend/internal/infra/windowsregister/turnstile_test.go`

- [ ] **Step 1: Write a fake page and failing send-code test**

```go
type fakePage struct {
    expressions []string
    results []json.RawMessage
    cookies []BrowserCookie
}

func TestSignupSendCodeUsesBrowserContext(t *testing.T) {
    page := &fakePage{results: []json.RawMessage{json.RawMessage(`"0"`)}}
    client := NewSignupClient(page, SignupConfig{})
    if err := client.SendCode(t.Context(), "a@b.test"); err != nil { t.Fatal(err) }
    if !strings.Contains(page.expressions[0], "CreateEmailValidationCode") {
        t.Fatalf("expression=%s", page.expressions[0])
    }
}
```

- [ ] **Step 2: Implement safe JavaScript argument passing**

Never interpolate email, code, password, proxy, or token into source strings. Use a constant function and pass values as Rod arguments:

```go
const browserFetchScript = `(args) => fetch(args.url, args.init).then(async r => ({
  status: r.status,
  retryAfter: r.headers.get('retry-after') || '',
  grpcStatus: r.headers.get('grpc-status') || '',
  text: await r.text()
}))`
```

Define `SendCode`, `VerifyCode`, and `Register` with response-size checks. Restrict extracted set-cookie URLs to HTTPS and an allowlist derived from `accounts.x.ai` and `x.ai`.

- [ ] **Step 3: Write failing Turnstile polling tests**

```go
func TestTurnstileSolverReturnsToken(t *testing.T) {
    page := &scriptedPage{values: []json.RawMessage{json.RawMessage(`""`), json.RawMessage(`"token-123"`)}}
    solver := TurnstileSolver{PollInterval: time.Millisecond, HardTimeout: time.Second}
    token, err := solver.Solve(t.Context(), page, "0x4AAAAAAAtest")
    if err != nil { t.Fatal(err) }
    if token != "token-123" { t.Fatalf("token=%q", token) }
}
```

- [ ] **Step 4: Implement bounded challenge handling**

Create the `.cf-turnstile` container, inject the official Turnstile script only from `https://challenges.cloudflare.com`, render with the discovered Site Key, poll `cf-turnstile-response`, and optionally click the visible widget center through the browser adapter. The solver must stop at `HardTimeout`, close its page, and return a typed `ErrTurnstileTimeout`.

- [ ] **Step 5: Test registration cookie and rate-limit classification**

Cover successful `sso`, missing cookie, invalid redirect host, 429/retry-after, challenge marker, and account denial. Verify errors and logs contain no email, password, code, or token.

- [ ] **Step 6: Run tests and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister -run "Signup|Turnstile" -count=1
git add internal/infra/windowsregister/signup* internal/infra/windowsregister/turnstile*
git commit -m "feat: implement native browser signup flow"
```

## Task 6: Compatible durable result store

**Files:**

- Create: `backend/internal/infra/windowsregister/store.go`
- Create: `backend/internal/infra/windowsregister/store_test.go`
- Modify: `backend/internal/infra/windowsregister/accounts.go`

- [ ] **Step 1: Write failing concurrent append test**

```go
func TestFileResultStoreConcurrentAppend(t *testing.T) {
    path := filepath.Join(t.TempDir(), "accounts.txt")
    store := NewFileResultStore(path)
    var wg sync.WaitGroup
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            if err := store.Append(Record{Email: fmt.Sprintf("u%d@x.test", i), Password: "p", SSO: fmt.Sprintf("sso-%d", i)}); err != nil {
                t.Error(err)
            }
        }(i)
    }
    wg.Wait()
    records, err := ReadAccountsFile(path)
    if err != nil { t.Fatal(err) }
    if len(records) != 20 { t.Fatalf("records=%d", len(records)) }
}
```

- [ ] **Step 2: Verify RED and implement the store**

```go
type ResultStore interface {
    Append(Record) error
    Read() ([]Record, error)
}
```

Use a mutex, `os.MkdirAll(..., 0700)`, `os.OpenFile(..., O_CREATE|O_APPEND|O_WRONLY, 0600)`, `bufio.Writer.Flush`, and `file.Sync`. Reject newline/colon characters in email/password and newline characters in SSO before writing.

- [ ] **Step 3: Run tests and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister -run "FileResultStore|ReadAccounts" -count=1
git add internal/infra/windowsregister/store* internal/infra/windowsregister/accounts.go
git commit -m "feat: persist native registration results safely"
```

## Task 7: Bounded S/P/C pipeline

**Files:**

- Create: `backend/internal/infra/windowsregister/pipeline.go`
- Create: `backend/internal/infra/windowsregister/pipeline_test.go`

- [ ] **Step 1: Define stage ports and write target/cancellation tests**

```go
type ChallengeProducer interface { Produce(context.Context) (ChallengeToken, error) }
type MailProducer interface { Produce(context.Context) (VerifiedMailbox, error) }
type AccountConsumer interface { Consume(context.Context, ChallengeToken, VerifiedMailbox) (Record, error) }

func TestPipelineStopsAtTarget(t *testing.T) {
    observer := &recordingObserver{}
    p := Pipeline{Challenges: fakeChallenges{}, Mail: fakeMail{}, Accounts: fakeAccounts{}, Observer: observer}
    if err := p.Run(t.Context(), PipelineOptions{Target: 3, SWorkers: 1, PWorkers: 1, CWorkers: 1, QueueSize: 2}); err != nil { t.Fatal(err) }
    if observer.SuccessCount() != 3 { t.Fatalf("success=%d", observer.SuccessCount()) }
}

func TestPipelineCancellationDoesNotLeakWorkers(t *testing.T) {
    ctx, cancel := context.WithCancel(t.Context())
    before := runtime.NumGoroutine()
    done := make(chan error, 1)
    go func() { done <- blockingPipeline().Run(ctx, PipelineOptions{Target: 10}) }()
    cancel()
    select {
    case <-done:
    case <-time.After(time.Second): t.Fatal("pipeline did not stop")
    }
    if runtime.NumGoroutine() > before+3 { t.Fatal("worker leak") }
}
```

- [ ] **Step 2: Verify RED and implement bounded channels**

Use three `errgroup.Group` worker sets and channels capped by `QueueSize`. Close channels only from their owning producer coordinator. Count success atomically; after reaching target, cancel the child context and allow an already completed record append to finish.

- [ ] **Step 3: Implement rate-limit circuit tests and behavior**

The first typed `ErrRateLimited` opens the circuit for configured cooldown, prevents new C work, and emits exactly one rate-limit observer event per open interval. Context cancellation bypasses cooldown waits.

- [ ] **Step 4: Run tests with race detector and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test -race ./internal/infra/windowsregister -run Pipeline -count=1
git add internal/infra/windowsregister/pipeline*
git commit -m "feat: add bounded native registration pipeline"
```

## Task 8: Native engine orchestration

**Files:**

- Create: `backend/internal/infra/windowsregister/engine.go`
- Create: `backend/internal/infra/windowsregister/engine_test.go`
- Create: `backend/internal/infra/windowsregister/flow.go`
- Create: `backend/internal/infra/windowsregister/flow_test.go`

- [ ] **Step 1: Write failing engine lifecycle test**

```go
func TestEngineLaunchesBrowserRunsPipelineAndCloses(t *testing.T) {
    browser := &fakeBrowser{}
    factory := &fakeBrowserFactory{Browser: browser}
    observer := &recordingObserver{}
    engine := NewEngine(EngineDependencies{Browsers: factory, Observer: observer, Store: memoryStore{}})
    err := engine.Run(t.Context(), StartOptions{Target: 1, EmailMode: "tempmail"})
    if err != nil { t.Fatal(err) }
    if !browser.Closed { t.Fatal("browser was not closed") }
    if observer.SuccessCount() != 1 { t.Fatalf("success=%d", observer.SuccessCount()) }
}
```

- [ ] **Step 2: Implement dependency assembly and run flow**

```go
type EngineDependencies struct {
    Browsers BrowserFactory
    Emails func(StartOptions) (EmailProvider, error)
    Store ResultStore
    Observer RunObserver
}

type Engine struct { deps EngineDependencies }
func (e *Engine) Run(ctx context.Context, opts StartOptions) error
```

`Run` launches one browser, discovers signup config, constructs challenge/mail/account stage adapters, runs the Pipeline, and defers browser closure. Recover panics at this boundary, convert them to `ErrEnginePanic`, and emit only a sanitized message.

- [ ] **Step 3: Add browser restart boundary**

Tests must show one browser crash causes one fresh launch and a second crash returns `ErrBrowserCrashed`. Do not retry registration rejection, invalid email, or context cancellation as browser crashes.

- [ ] **Step 4: Run engine tests and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister -run "Engine|Flow" -count=1
git add internal/infra/windowsregister/engine* internal/infra/windowsregister/flow*
git commit -m "feat: orchestrate native Windows registration engine"
```

## Task 9: Replace subprocess service while preserving API state

**Files:**

- Modify: `backend/internal/infra/windowsregister/service.go`
- Modify: `backend/internal/infra/windowsregister/service_test.go`
- Delete: `backend/internal/infra/windowsregister/process_windows.go`
- Delete: `backend/internal/infra/windowsregister/process_other.go`

- [ ] **Step 1: Replace process fake tests with runner tests before production edits**

```go
type Runner interface { Run(context.Context, StartOptions, RunObserver) error }

func TestServiceStartRunsNativeRunner(t *testing.T) {
    runner := &fakeRunner{Started: make(chan struct{})}
    svc := NewService(Config{Enabled: true, BrowserPath: touchExecutable(t), OutputDir: t.TempDir()})
    svc.SetRunner(runner)
    status, err := svc.Start(StartOptions{Target: 1, EmailMode: "tempmail"})
    if err != nil { t.Fatal(err) }
    <-runner.Started
    if !status.Running { t.Fatalf("status=%+v", status) }
}
```

- [ ] **Step 2: Verify RED and replace subprocess fields**

Remove `Process`, `ProcessFactory`, stdout scanning, worker environment, Python probes, and `osProcess`. Add `runCancel context.CancelFunc`, `runDone chan struct{}`, and `runner Runner`. Keep all public methods and JSON fields stable.

- [ ] **Step 3: Preserve counters and observer behavior**

The service implements `RunObserver`: success appends a record and increments success, failure increments failed, rate limit increments rateLimited, and log passes through `SanitizeLog`. Status continues deriving `GeneratedThisRun` from the result file baseline.

- [ ] **Step 4: Test stop, duplicate start, error, panic, and import compatibility**

Run:

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/infra/windowsregister ./internal/application/windowsregister ./internal/transport/http/account -count=1
```

Expected: PASS with unchanged HTTP response contracts.

- [ ] **Step 5: Delete process files and commit**

```powershell
git add backend/internal/infra/windowsregister
git commit -m "refactor: run Windows registration natively in Go"
```

## Task 10: Wire native dependencies and migrate configuration

**Files:**

- Modify: `backend/internal/app/application.go`
- Modify: `backend/internal/app/startup_test.go`
- Modify: `backend/internal/infra/config/config.go`
- Modify: `backend/internal/infra/config/config_test.go`
- Modify: `config.example.yaml`

- [ ] **Step 1: Write failing application assembly test**

Add a test config with `browserPath` and assert `newWindowsRegisterWorker` reports browser readiness without any Python or engine directory fixture.

- [ ] **Step 2: Replace `newWindowsRegisterWorker` assembly**

Construct:

```go
windowsregisterinfra.NewService(windowsregisterinfra.Config{
    Enabled: cfg.WindowsRegister.Enabled,
    BrowserPath: firstNonEmpty(os.Getenv("GROK2API_REGISTER_BROWSER"), cfg.WindowsRegister.BrowserPath),
    ManagedBrowserPath: filepath.Join(filepath.Dir(cfg.WindowsRegister.OutputDir), "browser", "chrome.exe"),
    OutputDir: cfg.WindowsRegister.OutputDir,
})
```

Remove `GROK2API_REGISTER_ENGINE` and `GROK2API_REGISTER_PYTHON` handling. Keep deprecated YAML fields loadable but unused.

- [ ] **Step 3: Update example config**

Use:

```yaml
windowsRegister:
  enabled: true
  # Empty = managed browser, then system Chrome/Edge.
  browserPath: ""
  outputDir: "./data/windows-register"
```

- [ ] **Step 4: Run app/config tests and commit**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./internal/app ./internal/infra/config ./internal/infra/windowsregister -count=1
git add internal/app/application.go internal/app/*_test.go internal/infra/config config.example.yaml
git commit -m "feat: wire native Windows registration runtime"
```

## Task 11: Remove Python packaging and finish Windows deployment

**Files:**

- Modify: `scripts/windows/package.ps1`
- Modify: `scripts/windows/deploy.ps1`
- Delete: `tools/windows-register/**`
- Modify: `.gitignore`
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `WINDOWS_DEPLOYMENT.md`

- [ ] **Step 1: Add package assertions before deleting Python**

Replace `Copy-WindowsRegisterEngine` with a stage validation that fails if any forbidden runtime appears:

```powershell
$forbidden = @(
    "tools\windows-register",
    ".venv",
    "requirements.txt"
)
foreach ($name in $forbidden) {
    if (Test-Path -LiteralPath (Join-Path $StagePath $name)) {
        throw "Native registration package contains forbidden Python runtime: $name"
    }
}
```

Update `BUILDINFO.txt` to `Windows register engine: native Go (external Chromium)`.

- [ ] **Step 2: Replace deploy Python setup with browser readiness**

Remove host Python discovery, venv creation, pip installation, module probes, CloakBrowser install, and Python environment variables. Implement `Resolve-RegistrationBrowser` using config/managed/system ordering. Core startup remains successful when no browser exists and prints one warning with the exact admin UI consequence.

If no system browser exists, download the pinned Chrome for Testing archive declared at the top of `deploy.ps1`, verify SHA-256, extract under `data/windows-register/browser`, and apply the same `LOCAL SERVICE` ACL used for data. Keep URL, version, and hash constants adjacent.

- [ ] **Step 3: Delete Python engine and update ignores**

Delete `tools/windows-register`. Remove its `.venv`, keys, logs, `.env`, and `__pycache__` ignore rules; retain `/data/` protection.

- [ ] **Step 4: Update documentation**

Document that Python is not required, system Chrome/Edge is preferred, managed Chromium is downloaded only if missing, and browser runtime remains the largest disk component. Preserve credential-data warnings.

- [ ] **Step 5: Run complete verification**

```powershell
cd backend
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe test ./... -count=1
& ..\.tools\go-go1.26.5-windows-amd64\go\bin\go.exe vet ./...
cd ..\frontend
pnpm lint
pnpm build
cd ..
$env:GROK2API_NO_PAUSE='1'
cmd.exe /d /c "call package.bat amd64"
```

Expected: all commands exit 0 and `release/grok2api-<version>-windows-amd64.zip` is produced.

- [ ] **Step 6: Inspect the ZIP and run browser smoke**

Expand the ZIP to a fresh `.tmp/native-register-smoke-*` directory. Assert recursively that it contains no `.py`, `.pyc`, `.venv`, `requirements.txt`, or `tools/windows-register`. Run `deploy.bat help`, start the binary with a generated safe test config on a free loopback port, and verify `/healthz` returns 200 without Python installed in PATH.

On an authorized Windows machine with a browser, set `GROK2API_TEST_BROWSER` and run the fixture integration test. Then perform the design-approved `target=1` live smoke and confirm current-result import.

- [ ] **Step 7: Commit final removal**

```powershell
git add -A
git commit -m "feat: replace Python registration worker with native Go"
```

## Final review checklist

- [ ] Existing admin endpoints and frontend decoders are unchanged.
- [ ] Deprecated YAML fields load but are not used.
- [ ] Service lifecycle tests cover start, stop, duplicate start, browser failure, and panic recovery.
- [ ] Pure parsers and protocol functions are fixture tested.
- [ ] Pipeline passes the race detector without leaks.
- [ ] Browser fixture test passes on Windows.
- [ ] Authorized `target=1` smoke passes before Python deletion is accepted.
- [ ] Full package contains no Python runtime or source.
- [ ] Core service starts without Python and without a browser; registration reports only missing browser.
- [ ] Logs, errors, and audits contain no email password, code, Turnstile token, SSO, proxy credentials, or CDP URL.
