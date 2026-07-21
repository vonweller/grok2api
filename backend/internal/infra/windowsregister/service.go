package windowsregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxLogLines = 300

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

var (
	ErrAlreadyRunning       = errors.New("windows register already running")
	ErrPlatformUnsupported  = errors.New("windows register is only supported on Windows")
	ErrNotReady             = errors.New("windows register runtime is not ready")
	ErrNoImportableAccounts = errors.New("no importable registration accounts")
	ErrInvalidStartOptions  = errors.New("invalid windows register start options")
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
	StartedAt         *string  `json:"startedAt,omitempty"`
	FinishedAt        *string  `json:"finishedAt,omitempty"`
	ElapsedSec        int      `json:"elapsedSec"`
	ExitCode          *int     `json:"exitCode,omitempty"`
	LastError         string   `json:"lastError"`
	Logs              []string `json:"logs"`
}

type Config struct {
	Enabled            bool
	BrowserPath        string
	ManagedBrowserPath string
	OutputDir          string
	EnginePath         string
	PythonPath         string
}

type Runner interface {
	Run(context.Context, StartOptions, RunObserver) error
}

type nativeRunner struct{ cfg Config }

func (r nativeRunner) Run(ctx context.Context, options StartOptions, observer RunObserver) error {
	browserPath := resolveBrowserPath(r.cfg.BrowserPath, r.cfg.ManagedBrowserPath, systemBrowserPaths())
	if browserPath == "" {
		return ErrNotReady
	}
	store := NewFileResultStore(filepath.Join(r.cfg.OutputDir, "accounts.txt"))
	engine := NewEngine(EngineDependencies{
		Browsers:       NewRodBrowserFactory(),
		BrowserOptions: BrowserLaunchOptions{ExecutablePath: browserPath},
		Emails: func(options StartOptions) (EmailProvider, error) {
			return NewEmailProvider(options.EmailMode, options.EmailAPI, options.EmailDomain, &http.Client{Timeout: emailRequestTimeout})
		},
		Store:    store,
		Observer: observer,
	})
	return engine.Run(ctx, options)
}

type Service struct {
	cfg              Config
	mu               sync.Mutex
	state            State
	target           int
	baseline         int
	successEvents    int
	failed           int
	rateLimited      int
	startedAt        time.Time
	finishedAt       time.Time
	exitCode         *int
	lastError        string
	stopRequested    bool
	logs             []string
	runCancel        context.CancelFunc
	runDone          chan struct{}
	platformOverride *bool
	skipProbes       bool
	runner           Runner
}

func NewService(cfg Config) *Service {
	cfg.BrowserPath = strings.TrimSpace(cfg.BrowserPath)
	cfg.ManagedBrowserPath = strings.TrimSpace(cfg.ManagedBrowserPath)
	cfg.OutputDir = strings.TrimSpace(cfg.OutputDir)
	if cfg.OutputDir == "" {
		cfg.OutputDir = filepath.Join("data", "windows-register")
	}
	if cfg.ManagedBrowserPath == "" {
		cfg.ManagedBrowserPath = filepath.Join(cfg.OutputDir, "browser", "chrome.exe")
	}
	return &Service{cfg: cfg, state: StateIdle, logs: make([]string, 0, maxLogLines), runner: nativeRunner{cfg: cfg}}
}

func (s *Service) SetRunner(runner Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runner == nil {
		s.runner = nativeRunner{cfg: s.cfg}
		return
	}
	s.runner = runner
}

func (s *Service) SetPlatformSupported(supported bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.platformOverride = &supported
}

func (s *Service) SetSkipRuntimeProbes(skip bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipProbes = skip
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Service) Start(options StartOptions) (Status, error) {
	if err := validateStartOptions(options); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	if !s.platformSupportedLocked() {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, ErrPlatformUnsupported
	}
	if s.runCancel != nil {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, ErrAlreadyRunning
	}
	ready, missing, _ := s.readinessLocked()
	if !ready {
		status := s.statusLocked()
		status.Missing = missing
		s.mu.Unlock()
		return status, fmt.Errorf("%w: %s", ErrNotReady, strings.Join(missing, ", "))
	}
	records, err := ReadAccountsFile(s.accountsPath())
	if err != nil {
		s.mu.Unlock()
		return Status{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runner := s.runner
	s.runCancel = cancel
	s.runDone = done
	s.state = StateRunning
	s.target = options.Target
	s.baseline = len(records)
	s.successEvents = 0
	s.failed = 0
	s.rateLimited = 0
	s.startedAt = time.Now().UTC()
	s.finishedAt = time.Time{}
	s.exitCode = nil
	s.lastError = ""
	s.stopRequested = false
	s.logs = s.logs[:0]
	s.appendLogLocked(fmt.Sprintf("[service] Windows 注册机启动中，目标 %d 个账号", options.Target))
	status := s.statusLocked()
	s.mu.Unlock()

	go s.run(ctx, done, runner, options)
	return status, nil
}

func (s *Service) run(ctx context.Context, done chan struct{}, runner Runner, options StartOptions) {
	var runErr error
	func() {
		defer func() {
			if recover() != nil {
				runErr = ErrEnginePanic
			}
		}()
		runErr = runner.Run(ctx, options, s)
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runDone != done {
		return
	}
	s.runCancel = nil
	s.runDone = nil
	s.finishedAt = time.Now().UTC()
	close(done)
	if s.stopRequested || errors.Is(runErr, context.Canceled) {
		s.state = StateStopped
		return
	}
	if runErr == nil {
		s.state = StateCompleted
		return
	}
	s.state = StateError
	s.lastError = SanitizeLog(runErr.Error())
}

func (s *Service) Stop(ctx context.Context) (Status, error) {
	s.mu.Lock()
	cancel := s.runCancel
	done := s.runDone
	if cancel == nil || done == nil {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, nil
	}
	s.stopRequested = true
	s.state = StateStopping
	s.appendLogLocked("[service] 正在停止注册机并清理浏览器进程")
	cancel()
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return s.Status(), ctx.Err()
	case <-done:
		return s.Status(), nil
	}
}

func (s *Service) ImportTokens(scope string) ([]string, error) {
	scope = strings.TrimSpace(strings.ToLower(scope))
	if scope == "" {
		scope = "current"
	}
	if scope != "current" && scope != "all" {
		return nil, fmt.Errorf("%w: scope must be current or all", ErrInvalidStartOptions)
	}
	s.mu.Lock()
	baseline := s.baseline
	path := s.accountsPath()
	s.mu.Unlock()
	records, err := ReadAccountsFile(path)
	if err != nil {
		return nil, err
	}
	tokens := SSOTokens(ScopeRecords(records, baseline, scope))
	if len(tokens) == 0 {
		return nil, ErrNoImportableAccounts
	}
	return tokens, nil
}

func (s *Service) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = s.Stop(ctx)
}

func (s *Service) Success(Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successEvents++
	s.appendLogLocked("[success] 注册成功")
}

func (s *Service) Failure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed++
	if err != nil {
		s.lastError = SanitizeLog(err.Error())
	}
	s.appendLogLocked("[failed] 注册步骤失败")
}

func (s *Service) RateLimited(time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateLimited++
	s.appendLogLocked("[rate-limit] 注册请求触发限流")
}

func (s *Service) Log(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLogLocked(SanitizeLog(line))
}

func (s *Service) appendLogLocked(line string) {
	if line == "" {
		return
	}
	if len(s.logs) >= maxLogLines {
		copy(s.logs, s.logs[1:])
		s.logs[len(s.logs)-1] = line
		return
	}
	s.logs = append(s.logs, line)
}

func (s *Service) statusLocked() Status {
	supported := s.platformSupportedLocked()
	ready, missing, browser := s.readinessLocked()
	records, _ := ReadAccountsFile(s.accountsPath())
	total := len(records)
	current := total - s.baseline
	if current < 0 {
		current = 0
	}
	success := current
	if s.successEvents > success {
		success = s.successEvents
	}
	percent := 0
	if s.target > 0 {
		percent = success * 100 / s.target
		if percent > 100 {
			percent = 100
		}
	}
	var startedAt, finishedAt *string
	if !s.startedAt.IsZero() {
		value := s.startedAt.Format(time.RFC3339)
		startedAt = &value
	}
	if !s.finishedAt.IsZero() {
		value := s.finishedAt.Format(time.RFC3339)
		finishedAt = &value
	}
	elapsed := 0
	if !s.startedAt.IsZero() {
		end := time.Now().UTC()
		if !s.finishedAt.IsZero() {
			end = s.finishedAt
		}
		elapsed = int(end.Sub(s.startedAt).Seconds())
		if elapsed < 0 {
			elapsed = 0
		}
	}
	if missing == nil {
		missing = []string{}
	}
	return Status{PlatformSupported: supported, Ready: ready, Missing: missing, BrowserInstalled: browser != "" || s.skipProbes, State: s.state, Running: s.runCancel != nil, Target: s.target, Success: success, Failed: s.failed, RateLimited: s.rateLimited, Percent: percent, GeneratedThisRun: current, GeneratedTotal: total, CanImportCurrent: current > 0, CanImportAll: total > 0, StartedAt: startedAt, FinishedAt: finishedAt, ElapsedSec: elapsed, ExitCode: s.exitCode, LastError: s.lastError, Logs: append([]string(nil), s.logs...)}
}

func (s *Service) platformSupportedLocked() bool {
	if s.platformOverride != nil {
		return *s.platformOverride
	}
	return s.cfg.Enabled && PlatformSupported()
}

func (s *Service) readinessLocked() (bool, []string, string) {
	if !s.platformSupportedLocked() {
		return false, []string{"windows"}, ""
	}
	browser := resolveBrowserPath(s.cfg.BrowserPath, s.cfg.ManagedBrowserPath, systemBrowserPaths())
	if browser == "" && !s.skipProbes {
		return false, []string{"browser"}, ""
	}
	return true, nil, browser
}

func (s *Service) accountsPath() string { return filepath.Join(s.cfg.OutputDir, "accounts.txt") }

func validateStartOptions(options StartOptions) error {
	if options.Target < 1 || options.Target > 10000 {
		return fmt.Errorf("%w: target must be 1..10000", ErrInvalidStartOptions)
	}
	mode := strings.ToLower(strings.TrimSpace(options.EmailMode))
	if mode == "" {
		mode = "tempmail"
	}
	if mode != "tempmail" && mode != "custom" {
		return fmt.Errorf("%w: emailMode must be tempmail or custom", ErrInvalidStartOptions)
	}
	if mode == "custom" && (strings.TrimSpace(options.EmailAPI) == "" || strings.TrimSpace(options.EmailDomain) == "") {
		return fmt.Errorf("%w: custom email mode requires emailApi and emailDomain", ErrInvalidStartOptions)
	}
	if len(options.Proxy) > 512 || len(options.MaxMem) > 32 {
		return fmt.Errorf("%w: proxy or maxMem too long", ErrInvalidStartOptions)
	}
	return nil
}
