# Windows Register WebUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed `grok-free-register` as a Windows-only managed worker inside `grok2api`, with admin WebUI start/stop/status/import into existing Web/Console account pools.

**Architecture:** Go owns a single Python subprocess under `tools/windows-register`, exposes admin APIs, sanitizes logs, and reuses existing account import services. React Registration page gains a Windows register panel. Non-Windows returns unavailable.

**Tech Stack:** Go 1.26 + Gin, React 19 + TypeScript, Python register engine (CloakBrowser/Playwright), existing account import pipeline.

**Spec:** `docs/superpowers/specs/2026-07-19-windows-register-webui-design.md`

---

## File map

| Path | Responsibility |
| --- | --- |
| `tools/windows-register/**` | Vendored/adapted Python engine + setup scripts |
| `backend/internal/infra/windowsregister/` | Process lifecycle, readiness, log sanitize, accounts parse |
| `backend/internal/application/windowsregister/` | Start validation, import orchestration |
| `backend/internal/transport/http/account/` | HTTP routes + DTOs |
| `backend/internal/transport/http/server.go` | Wire deps |
| `backend/internal/app/application.go` | Construct service, shutdown hook |
| `backend/internal/infra/config/config.go` | Optional `windowsRegister` config |
| `frontend/src/features/registration/windows-register-api.ts` | Admin API client |
| `frontend/src/features/registration/registration-page.tsx` | UI panel |
| `frontend/src/shared/i18n/index.ts` | Resolve conflict + strings |
| `scripts/windows/package.ps1` | Include engine source, exclude venv/keys |
| `WINDOWS_DEPLOYMENT.md`, `README*.md` | Operator docs |

---

### Task 1: Resolve i18n merge conflict and registration baseline

**Files:**
- Modify: `frontend/src/shared/i18n/index.ts`

- [ ] **Step 1: Inspect both sides of the conflict**

```bash
git show :2:frontend/src/shared/i18n/index.ts | rg -n "registration|updates:|noteTitle|manualOnly" | head -40
git show :3:frontend/src/shared/i18n/index.ts | rg -n "registration|updates:|noteTitle|manualOnly" | head -40
```

- [ ] **Step 2: Merge manually**

Keep:
- `nav.registration` (zh + en)
- full `registration: { ... }` blocks from the registration branch (theirs/stage 3 style)
- newer `updates` note fields from ours if present (`noteTitle` / `noteDescription`)
- do not drop unrelated keys from either side

Remove all conflict markers:
```text
<<<<<<<
=======
>>>>>>>
```

- [ ] **Step 3: Mark resolved and commit**

```bash
git add frontend/src/shared/i18n/index.ts
git commit -m "fix: resolve registration i18n merge conflict"
```

---

### Task 2: Vendor and adapt the Python engine

**Files:**
- Create: `tools/windows-register/**`
- Modify: `.gitignore`

- [ ] **Step 1: Copy engine from the REGISTER_OUTPUT_DIR-capable source**

Prefer the bundle-adapted engine (already supports output dir):

```bash
SRC="C:/Users/Administrator/Documents/Codex/2026-07-16/j/outputs/grok-windows-bundle/grokcli-2api/grok_register"
OSS="C:/Users/Administrator/Documents/Codex/2026-07-16/j/outputs/grok-windows-bundle/grok-free-register-oss"
mkdir -p tools/windows-register
# copy package tree
cp -a "$SRC" tools/windows-register/grok_register
# requirements + windows setup from OSS
cp "$OSS/requirements.txt" tools/windows-register/
cp "$OSS/setup.ps1" "$OSS/setup.cmd" "$OSS/start.ps1" "$OSS/start.cmd" tools/windows-register/ 2>/dev/null || true
cp "$OSS/README_WINDOWS.md" tools/windows-register/README.md
cp "$OSS/LICENSE" tools/windows-register/LICENSE 2>/dev/null || true
# remove caches
find tools/windows-register -type d -name '__pycache__' -prune -exec rm -rf {} +
find tools/windows-register -type d -name '.venv' -prune -exec rm -rf {} +
```

If `grokcli-2api/grok_register` lacks `core/`, copy core from OSS:

```bash
cp -a "$OSS/grok_register/core" tools/windows-register/grok_register/
cp "$OSS/grok_register/clearance.py" "$OSS/grok_register/email_server.py" "$OSS/grok_register/__init__.py" tools/windows-register/grok_register/
```

- [ ] **Step 2: Verify REGISTER_OUTPUT_DIR support**

```bash
rg -n "REGISTER_OUTPUT_DIR|_registration_output_path" tools/windows-register/grok_register/register.py
```

Expected: env-based output dir, not hard-coded only `keys/accounts.txt`.

If missing, patch top of `register.py` to:

```python
REGISTER_OUTPUT_DIR = os.path.abspath(
    os.path.expanduser(os.environ.get("REGISTER_OUTPUT_DIR") or "keys")
)
os.makedirs(REGISTER_OUTPUT_DIR, exist_ok=True)

def _registration_output_path(name):
    return os.path.join(REGISTER_OUTPUT_DIR, name)
```

and route account writes through `_registration_output_path("accounts.txt")` / `grok.txt`.

- [ ] **Step 3: Add package README note**

In `tools/windows-register/README.md`, document:
- managed by grok2api admin API
- setup via `setup.ps1`
- output defaults to `data/windows-register` when launched by Go
- source attribution path

- [ ] **Step 4: Gitignore engine runtime junk**

Append to `.gitignore`:

```gitignore
# Windows register engine runtime
/tools/windows-register/.venv/
/tools/windows-register/keys/
/tools/windows-register/logs/
/tools/windows-register/.env
/tools/windows-register/**/__pycache__/
```

- [ ] **Step 5: Commit**

```bash
git add tools/windows-register .gitignore
git commit -m "chore: vendor Windows register engine under tools/windows-register"
```

---

### Task 3: Infra — sanitize, parse, readiness helpers (TDD)

**Files:**
- Create: `backend/internal/infra/windowsregister/sanitize.go`
- Create: `backend/internal/infra/windowsregister/accounts.go`
- Create: `backend/internal/infra/windowsregister/sanitize_test.go`
- Create: `backend/internal/infra/windowsregister/accounts_test.go`

- [ ] **Step 1: Write failing sanitize tests**

```go
package windowsregister_test

import (
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

func TestSanitizeRegistrationLogHidesSecrets(t *testing.T) {
	in := "sso=eyJhbGciOiJIUzI1NiJ9.aaa.bbb password=secret user@example.com http://u:p@127.0.0.1:7890"
	out := windowsregister.SanitizeLog(in)
	if strings.Contains(out, "eyJ") || strings.Contains(out, "secret") || strings.Contains(out, "user@") || strings.Contains(out, "u:p@") {
		t.Fatalf("secrets leaked: %q", out)
	}
	if !strings.Contains(out, "[token hidden]") && !strings.Contains(out, "[hidden]") {
		t.Fatalf("expected redaction markers: %q", out)
	}
}
```

- [ ] **Step 2: Write failing accounts tests**

```go
func TestReadRegistrationRecordsAndScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.txt")
	content := "a@x.com:pw1:sso1\nbad\nc@x.com:pw2:sso2\na@x.com:pw1:sso1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := windowsregister.ReadAccountsFile(path)
	if err != nil || len(records) != 2 {
		t.Fatalf("got %v err=%v", records, err)
	}
	current := windowsregister.ScopeRecords(records, 1, "current")
	if len(current) != 1 || current[0].SSO != "sso2" {
		t.Fatalf("scope current failed: %+v", current)
	}
	tokens := windowsregister.SSOTokens(current)
	if len(tokens) != 1 || tokens[0] != "sso2" {
		t.Fatalf("tokens=%v", tokens)
	}
}
```

- [ ] **Step 3: Implement helpers**

`sanitize.go`: ANSI strip, JWT, secret key=value, proxy userinfo, email mask, 2000 cap.  
`accounts.go`:

```go
type Record struct {
	Email, Password, SSO string
}

func ReadAccountsFile(path string) ([]Record, error)
func ScopeRecords(records []Record, baseline int, scope string) []Record
func SSOTokens(records []Record) []string
func ClassifyLogLine(line string) (success, failed, rateLimited bool)
```

- [ ] **Step 4: Run tests**

```bash
cd backend && go test ./internal/infra/windowsregister/ -count=1
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/infra/windowsregister
git commit -m "feat: add windows register log sanitize and accounts parsing"
```

---

### Task 4: Infra — process service + status machine (TDD)

**Files:**
- Create: `backend/internal/infra/windowsregister/service.go`
- Create: `backend/internal/infra/windowsregister/service_test.go`
- Create: `backend/internal/infra/windowsregister/paths.go`

- [ ] **Step 1: Define public types in service.go**

```go
type State string

const (
	StateIdle      State = "idle"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateStopping  State = "stopping"
	StateStopped   State = "stopped"
	StateCompleted State = "completed"
	StateError     State = "error"
)

type StartOptions struct {
	Target      int
	EmailMode   string
	EmailAPI    string
	EmailDomain string
	Proxy       string
	MaxMem      string
	Debug       bool
}

type Status struct {
	PlatformSupported bool     `json:"platformSupported"`
	Ready             bool     `json:"ready"`
	Missing           []string `json:"missing"`
	BrowserInstalled  bool     `json:"browserInstalled"`
	State             State    `json:"state"`
	Running           bool     `json:"running"`
	Target            int      `json:"target"`
	Success           int      `json:"success"`
	Failed            int      `json:"failed"`
	RateLimited       int      `json:"rateLimited"`
	Percent           int      `json:"percent"`
	GeneratedThisRun  int      `json:"generatedThisRun"`
	GeneratedTotal    int      `json:"generatedTotal"`
	CanImportCurrent  bool     `json:"canImportCurrent"`
	CanImportAll      bool     `json:"canImportAll"`
	StartedAt         *string  `json:"startedAt"`
	FinishedAt        *string  `json:"finishedAt"`
	ElapsedSec        int      `json:"elapsedSec"`
	ExitCode          *int     `json:"exitCode"`
	LastError         string   `json:"lastError"`
	Logs              []string `json:"logs"`
}

type Config struct {
	Enabled    bool
	EnginePath string
	OutputDir  string
	PythonPath string
}

type ProcessFactory func(ctx context.Context, name string, arg ...string) (Process, error)

type Process interface {
	Start() error
	StdoutPipe() (io.ReadCloser, error)
	Wait() error
	KillTree() error
	Pid() int
	// platform helpers may be concrete on *osProcess
}
```

Use a test double process that emits lines and exits.

- [ ] **Step 2: Failing tests for single-flight and counters**

```go
func TestStartRejectsWhenRunning(t *testing.T) { /* fake long-running process; second Start returns ErrAlreadyRunning */ }
func TestStatusCountsFromLogsAndAccounts(t *testing.T) { /* write accounts file + emit 注册成功 lines */ }
func TestNonWindowsNotSupported(t *testing.T) { /* force platformSupported=false via test hook or build tag helper */ }
```

For non-Windows CI, implement:

```go
func PlatformSupported() bool { return runtime.GOOS == "windows" }
```

and unit-test Start returns `ErrPlatformUnsupported` when a `forceUnsupported` test option is set, or test only the pure helper.

- [ ] **Step 3: Implement Service**

Key methods:

```go
func NewService(cfg Config) *Service
func (s *Service) Status() Status
func (s *Service) Start(opts StartOptions) (Status, error)
func (s *Service) Stop(ctx context.Context) (Status, error)
func (s *Service) ImportTokens(scope string) ([]string, error)
func (s *Service) Close() // best-effort stop on shutdown
```

Errors:

```go
var (
	ErrAlreadyRunning       = errors.New("windows register already running")
	ErrPlatformUnsupported  = errors.New("windows register is only supported on Windows")
	ErrNotReady             = errors.New("windows register runtime is not ready")
	ErrNoImportableAccounts = errors.New("no importable registration accounts")
)
```

Start must:
1. lock
2. reject non-windows / not ready / already running
3. ensure output dir
4. baseline = len(accounts)
5. build command `python -u -m grok_register.register --target N`
6. set env REGISTER_OUTPUT_DIR, EMAIL_*, REGISTER_PROXY, PYTHONUTF8, PYTHONUNBUFFERED
7. set cwd=enginePath
8. stream logs into ring buffer (300), classify counters
9. on wait complete set completed/error

Stop must CTRL_BREAK / kill tree on Windows; terminate+kill elsewhere for tests.

Readiness missing keys: `windows`, `engine`, `python`, `playwright`/`cloakbrowser` (if cheap probe fails), `browser`.

Python resolve order from design.

- [ ] **Step 4: Run tests**

```bash
cd backend && go test ./internal/infra/windowsregister/ -count=1
```

- [ ] **Step 5: Commit**

```bash
git add backend/internal/infra/windowsregister
git commit -m "feat: manage Windows register Python worker lifecycle"
```

---

### Task 5: Application layer import orchestration

**Files:**
- Create: `backend/internal/application/windowsregister/service.go`
- Create: `backend/internal/application/windowsregister/service_test.go`

- [ ] **Step 1: Define app service**

```go
type AccountImporter interface {
	ImportWebCredentials(ctx context.Context, data []byte) (accountapp.ImportResult, error)
	ImportConsoleCredentials(ctx context.Context, data []byte) (accountapp.ImportResult, error)
}

type Service struct {
	worker   *windowsregister.Service
	accounts AccountImporter
}

type ImportRequest struct {
	Scope         string
	Destinations  []string // default web+console
}

type ProviderImportResult struct {
	Provider   string `json:"provider"`
	Created    int    `json:"created"`
	Updated    int    `json:"updated"`
	Skipped    int    `json:"skipped"`
	// account IDs optional; keep small
	Error      string `json:"error,omitempty"`
}

type ImportResponse struct {
	Scope       string                 `json:"scope"`
	SourceCount int                    `json:"sourceCount"`
	Results     []ProviderImportResult `json:"results"`
}
```

- [ ] **Step 2: Implement Import**

```go
tokens, err := s.worker.ImportTokens(req.Scope)
payload := []byte(strings.Join(tokens, "\n"))
// for each destination call importer
```

Validate destinations ∈ {`grok_web`,`grok_console`}.

- [ ] **Step 3: Tests with fake importer + fake worker tokens**

Prefer small interface on worker:

```go
type TokenSource interface {
	ImportTokens(scope string) ([]string, error)
	Status() windowsregister.Status
	Start(windowsregister.StartOptions) (windowsregister.Status, error)
	Stop(context.Context) (windowsregister.Status, error)
}
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/application/windowsregister
git commit -m "feat: orchestrate windows register import into account pools"
```

---

### Task 6: HTTP routes + wiring

**Files:**
- Modify: `backend/internal/transport/http/account/handler.go`
- Create: `backend/internal/transport/http/account/windows_register.go`
- Create: `backend/internal/transport/http/account/windows_register_test.go`
- Modify: `backend/internal/transport/http/server.go`
- Modify: `backend/internal/app/application.go`
- Modify: `backend/internal/infra/config/config.go` (+ example yaml if needed)

- [ ] **Step 1: Extend Handler**

```go
type Handler struct {
	service         *accountapp.Service
	sync            accountSynchronizer
	windowsRegister *windowsregisterapp.Service // nullable
}

func NewHandler(service *accountapp.Service, sync accountSynchronizer, windowsRegister ...*windowsregisterapp.Service) *Handler
```

Register routes:

```go
router.GET("/accounts/windows-register/status", h.windowsRegisterStatus)
router.POST("/accounts/windows-register/start", h.windowsRegisterStart)
router.POST("/accounts/windows-register/stop", h.windowsRegisterStop)
router.POST("/accounts/windows-register/import", h.windowsRegisterImport)
```

If `windowsRegister == nil`, status returns platformSupported/ready false; mutating endpoints 503.

- [ ] **Step 2: Map errors**

| error | status | code |
| --- | --- | --- |
| ErrPlatformUnsupported / nil service | 503 | windowsRegisterUnavailable |
| ErrNotReady | 503 | windowsRegisterNotReady |
| ErrAlreadyRunning | 409 | windowsRegisterRunning |
| validation | 400 | invalidRequest |
| ErrNoImportableAccounts | 400 | windowsRegisterEmpty |

Use `response.Success` / `response.Error`.

- [ ] **Step 3: Wire in application.go**

```go
wrCfg := windowsregister.Config{
	Enabled: runtime.GOOS == "windows",
	EnginePath: filepath.Join(projectRootOrConfigDir, "tools", "windows-register"),
	OutputDir: filepath.Join(configDir, "data", "windows-register"),
}
wrInfra := windowsregister.NewService(wrCfg)
wrApp := windowsregisterapp.NewService(wrInfra, accountService)
// pass to httpserver.Dependencies
// on shutdown: wrInfra.Close()
```

Resolve paths relative to config file directory (same as sqlite/media).

- [ ] **Step 4: HTTP tests**

- unauthenticated → 401/redirect existing behavior
- start invalid target → 400
- status shape when service nil

- [ ] **Step 5: `go test` affected packages**

```bash
cd backend && go test ./internal/transport/http/account/ ./internal/application/windowsregister/ ./internal/infra/windowsregister/ -count=1
```

- [ ] **Step 6: Commit**

```bash
git add backend
git commit -m "feat: expose windows register admin APIs"
```

---

### Task 7: Frontend API + Registration panel

**Files:**
- Create: `frontend/src/features/registration/windows-register-api.ts`
- Modify: `frontend/src/features/registration/registration-page.tsx`
- Modify: `frontend/src/shared/i18n/index.ts`

- [ ] **Step 1: API client**

Mirror existing `accounts-api.ts` patterns (`apiRequest`, decoders if used lightly).

Endpoints under `/api/admin/v1/accounts/windows-register/*`.

- [ ] **Step 2: Panel UI**

In `registration-page.tsx`:
- query status on mount
- poll every 1500ms while `running|starting|stopping`
- form fields: target, emailMode, emailApi, emailDomain, proxy, maxMem, debug
- buttons: start/stop/import current/import all
- show missing readiness chips
- log `<pre>` box with sanitized logs
- hide controls when `!platformSupported`

Keep existing official/device/manual import sections.

- [ ] **Step 3: i18n keys**

Add `registration.windows*` keys (zh+en) for labels, states, errors, missing components.

- [ ] **Step 4: Lint if feasible**

```bash
cd frontend && pnpm lint
```

- [ ] **Step 5: Commit**

```bash
git add frontend
git commit -m "feat: add Windows register controls to registration page"
```

---

### Task 8: Packaging + docs

**Files:**
- Modify: `scripts/windows/package.ps1`
- Modify: `WINDOWS_DEPLOYMENT.md`
- Modify: `README.md`, `README.zh-CN.md`
- Modify: `config.example.yaml` (optional windowsRegister block)

- [ ] **Step 1: package.ps1 whitelist**

Include `tools/windows-register` source files; exclude:

```text
.venv, keys, logs, .env, __pycache__, *.pyc
```

Verify stage contains `tools/windows-register/grok_register/register.py` and `requirements.txt`.

- [ ] **Step 2: Docs**

Document:
1. install Python 3.10+
2. run `tools/windows-register/setup.ps1`
3. start grok2api
4. open Registration → Windows register panel
5. import results into account pool
6. data lives in `data/windows-register` (sensitive)

- [ ] **Step 3: Commit**

```bash
git add scripts/windows/package.ps1 WINDOWS_DEPLOYMENT.md README.md README.zh-CN.md config.example.yaml
git commit -m "docs: document Windows register packaging and setup"
```

---

### Task 9: End-to-end verification checklist

- [ ] **Step 1: Unit tests**

```bash
cd backend && go test ./internal/infra/windowsregister/ ./internal/application/windowsregister/ ./internal/transport/http/account/ -count=1
```

- [ ] **Step 2: Frontend typecheck/lint if tools present**

```bash
cd frontend && pnpm exec tsc --noEmit
```

- [ ] **Step 3: Manual Windows smoke (when environment ready)**

1. `tools/windows-register/setup.ps1 -SmokeTest`
2. start backend with local config
3. GET status → ready true after setup
4. start target=1 only if network/proxy authorized
5. confirm logs redacted
6. import current → accounts list grows

- [ ] **Step 4: Final commit if fixes needed**

---

## Self-review vs spec

| Spec section | Task |
| --- | --- |
| WebUI built-in | Task 7 |
| Go manages Python subprocess | Task 4–6 |
| Windows only | Task 4, 6, 7 |
| Register + import only | Task 5–7 |
| REGISTER_OUTPUT_DIR | Task 2 |
| Sanitize logs | Task 3 |
| package include engine exclude secrets | Task 8 |
| i18n conflict | Task 1 |
| No xai_enroller / email service UI | intentionally omitted |

No TBD placeholders remain in this plan.
