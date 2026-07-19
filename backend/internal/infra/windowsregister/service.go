package windowsregister

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxLogLines = 300

// Worker lifecycle states exposed to the admin API.
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

// StartOptions configures one registration run.
type StartOptions struct {
	Target      int
	EmailMode   string
	EmailAPI    string
	EmailDomain string
	Proxy       string
	MaxMem      string
	Debug       bool
}

// Status is a credential-free snapshot for the admin UI.
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

// Config locates the engine and output directories.
type Config struct {
	Enabled    bool
	EnginePath string
	OutputDir  string
	PythonPath string
}

// Process is the minimal subprocess surface used by Service.
type Process interface {
	Start() error
	StdoutPipe() (io.ReadCloser, error)
	Wait() error
	KillTree() error
	Pid() int
	ExitCode() int
}

// ProcessFactory creates a worker process. Tests inject fakes.
type ProcessFactory func(ctx context.Context, name string, arg ...string) (Process, error)

// Service owns at most one registration worker.
type Service struct {
	cfg Config

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
	process          Process
	platformOverride *bool
	skipProbes       bool
	factory          ProcessFactory
}

// NewService constructs a managed registration worker service.
func NewService(cfg Config) *Service {
	cfg.EnginePath = strings.TrimSpace(cfg.EnginePath)
	cfg.OutputDir = strings.TrimSpace(cfg.OutputDir)
	cfg.PythonPath = strings.TrimSpace(cfg.PythonPath)
	return &Service{
		cfg:     cfg,
		state:   StateIdle,
		logs:    make([]string, 0, maxLogLines),
		factory: defaultProcessFactory,
	}
}

// SetProcessFactory replaces the process constructor (tests).
func (s *Service) SetProcessFactory(factory ProcessFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if factory == nil {
		s.factory = defaultProcessFactory
		return
	}
	s.factory = factory
}

// SetPlatformSupported overrides platform detection (tests).
func (s *Service) SetPlatformSupported(supported bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.platformOverride = &supported
}

// SetSkipRuntimeProbes disables python package import probes (tests).
func (s *Service) SetSkipRuntimeProbes(skip bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipProbes = skip
}

// Status returns the current worker snapshot.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// Start launches the registration worker.
func (s *Service) Start(opts StartOptions) (Status, error) {
	if err := validateStartOptions(opts); err != nil {
		return Status{}, err
	}

	s.mu.Lock()
	if !s.platformSupportedLocked() {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, ErrPlatformUnsupported
	}
	if s.process != nil {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, ErrAlreadyRunning
	}
	ready, missing, browserPath := s.readinessLocked()
	if !ready {
		status := s.statusLocked()
		status.Missing = missing
		s.mu.Unlock()
		return status, fmt.Errorf("%w: %s", ErrNotReady, strings.Join(missing, ", "))
	}
	if err := os.MkdirAll(s.cfg.OutputDir, 0o700); err != nil {
		s.mu.Unlock()
		return Status{}, err
	}
	records, _ := ReadAccountsFile(s.accountsPath())
	baseline := len(records)

	python := resolvePython(s.cfg.PythonPath, s.cfg.EnginePath)
	args := []string{"-u", "-m", "grok_register.register", "--target", strconv.Itoa(opts.Target)}
	if strings.TrimSpace(opts.MaxMem) != "" {
		args = append(args, "--max-mem", strings.TrimSpace(opts.MaxMem))
	}
	if opts.Debug {
		args = append(args, "--debug")
	}

	factory := s.factory
	enginePath := s.cfg.EnginePath
	outputDir := s.cfg.OutputDir
	s.state = StateStarting
	s.target = opts.Target
	s.baseline = baseline
	s.successEvents = 0
	s.failed = 0
	s.rateLimited = 0
	s.startedAt = time.Now().UTC()
	s.finishedAt = time.Time{}
	s.exitCode = nil
	s.lastError = ""
	s.stopRequested = false
	s.logs = s.logs[:0]
	s.appendLogLocked(fmt.Sprintf("[service] Windows 注册机启动中，目标 %d 个账号", opts.Target))
	s.mu.Unlock()

	ctx := context.Background()
	proc, err := factory(ctx, python, args...)
	if err != nil {
		s.mu.Lock()
		s.state = StateError
		s.finishedAt = time.Now().UTC()
		s.lastError = SanitizeLog(err.Error())
		status := s.statusLocked()
		s.mu.Unlock()
		return status, err
	}
	if concrete, ok := proc.(*osProcess); ok {
		concrete.cmd.Dir = enginePath
		concrete.cmd.Env = buildWorkerEnv(opts, outputDir, browserPath)
	}

	stdout, err := proc.StdoutPipe()
	if err != nil {
		_ = proc.KillTree()
		s.mu.Lock()
		s.state = StateError
		s.finishedAt = time.Now().UTC()
		s.lastError = SanitizeLog(err.Error())
		status := s.statusLocked()
		s.mu.Unlock()
		return status, err
	}
	if err := proc.Start(); err != nil {
		_ = stdout.Close()
		s.mu.Lock()
		s.state = StateError
		s.finishedAt = time.Now().UTC()
		s.lastError = SanitizeLog(err.Error())
		status := s.statusLocked()
		s.mu.Unlock()
		return status, err
	}

	s.mu.Lock()
	s.process = proc
	s.state = StateRunning
	s.mu.Unlock()

	go s.consume(proc, stdout)
	return s.Status(), nil
}

// Stop requests worker termination.
func (s *Service) Stop(ctx context.Context) (Status, error) {
	s.mu.Lock()
	proc := s.process
	if proc == nil {
		status := s.statusLocked()
		s.mu.Unlock()
		return status, nil
	}
	s.stopRequested = true
	s.state = StateStopping
	s.appendLogLocked("[service] 正在停止注册机并清理浏览器进程")
	s.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- proc.KillTree()
	}()
	select {
	case <-ctx.Done():
		return s.Status(), ctx.Err()
	case <-done:
	case <-time.After(15 * time.Second):
	}
	return s.Status(), nil
}

// ImportTokens returns SSO tokens for the requested scope.
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
	selected := ScopeRecords(records, baseline, scope)
	tokens := SSOTokens(selected)
	if len(tokens) == 0 {
		return nil, ErrNoImportableAccounts
	}
	return tokens, nil
}

// Close stops any running worker during process shutdown.
func (s *Service) Close() {
	_, _ = s.Stop(context.Background())
}

func (s *Service) consume(proc Process, stdout io.ReadCloser) {
	defer stdout.Close()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		s.consumeLine(scanner.Text())
	}
	waitErr := proc.Wait()
	code := proc.ExitCode()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != proc {
		return
	}
	s.process = nil
	s.exitCode = &code
	s.finishedAt = time.Now().UTC()
	if s.stopRequested {
		s.state = StateStopped
		return
	}
	if waitErr == nil && code == 0 {
		s.state = StateCompleted
		return
	}
	s.state = StateError
	if s.lastError == "" {
		if waitErr != nil {
			s.lastError = SanitizeLog(waitErr.Error())
		} else {
			s.lastError = fmt.Sprintf("注册进程退出码 %d", code)
		}
	}
}

func (s *Service) consumeLine(line string) {
	safe := SanitizeLog(line)
	if safe == "" {
		return
	}
	success, failed, rateLimited := ClassifyLogLine(safe)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLogLocked(safe)
	if success {
		s.successEvents++
	}
	if failed {
		s.failed++
	}
	if rateLimited {
		s.rateLimited++
	}
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
	ready, missing, _ := s.readinessLocked()
	records, _ := ReadAccountsFile(s.accountsPath())
	generatedTotal := len(records)
	generatedThisRun := generatedTotal - s.baseline
	if generatedThisRun < 0 {
		generatedThisRun = 0
	}
	success := generatedThisRun
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
	running := s.process != nil && (s.state == StateRunning || s.state == StateStarting || s.state == StateStopping)
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
	logs := append([]string{}, s.logs...)
	if missing == nil {
		missing = []string{}
	}
	return Status{
		PlatformSupported: supported,
		Ready:             ready,
		Missing:           missing,
		BrowserInstalled:  resolveBrowserPath(s.cfg.EnginePath) != "" || s.skipProbes,
		State:             s.state,
		Running:           running,
		Target:            s.target,
		Success:           success,
		Failed:            s.failed,
		RateLimited:       s.rateLimited,
		Percent:           percent,
		GeneratedThisRun:  generatedThisRun,
		GeneratedTotal:    generatedTotal,
		CanImportCurrent:  generatedThisRun > 0,
		CanImportAll:      generatedTotal > 0,
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		ElapsedSec:        elapsed,
		ExitCode:          s.exitCode,
		LastError:         s.lastError,
		Logs:              logs,
	}
}

func (s *Service) platformSupportedLocked() bool {
	if s.platformOverride != nil {
		return *s.platformOverride
	}
	if !s.cfg.Enabled {
		return false
	}
	return PlatformSupported()
}

func (s *Service) readinessLocked() (bool, []string, string) {
	missing := make([]string, 0, 4)
	if !s.platformSupportedLocked() {
		missing = append(missing, "windows")
		return false, missing, ""
	}
	if !enginePresent(s.cfg.EnginePath) {
		missing = append(missing, "engine")
	}
	python := resolvePython(s.cfg.PythonPath, s.cfg.EnginePath)
	if python == "" {
		missing = append(missing, "python")
	}
	browser := resolveBrowserPath(s.cfg.EnginePath)
	if !s.skipProbes {
		if browser == "" {
			missing = append(missing, "browser")
		}
		if python != "" {
			if !probePythonModule(python, s.cfg.EnginePath, "playwright") {
				missing = append(missing, "playwright")
			}
			if !probePythonModule(python, s.cfg.EnginePath, "cloakbrowser") {
				missing = append(missing, "cloakbrowser")
			}
		}
	} else if python == "" {
		// already recorded
	}
	return len(missing) == 0, missing, browser
}

func (s *Service) accountsPath() string {
	return filepath.Join(s.cfg.OutputDir, "accounts.txt")
}

func validateStartOptions(opts StartOptions) error {
	if opts.Target < 1 || opts.Target > 10000 {
		return fmt.Errorf("%w: target must be 1..10000", ErrInvalidStartOptions)
	}
	mode := strings.TrimSpace(strings.ToLower(opts.EmailMode))
	if mode == "" {
		mode = "tempmail"
	}
	if mode != "tempmail" && mode != "custom" {
		return fmt.Errorf("%w: emailMode must be tempmail or custom", ErrInvalidStartOptions)
	}
	if mode == "custom" {
		if strings.TrimSpace(opts.EmailAPI) == "" || strings.TrimSpace(opts.EmailDomain) == "" {
			return fmt.Errorf("%w: custom email mode requires emailApi and emailDomain", ErrInvalidStartOptions)
		}
	}
	if len(opts.Proxy) > 512 || len(opts.MaxMem) > 32 {
		return fmt.Errorf("%w: proxy or maxMem too long", ErrInvalidStartOptions)
	}
	return nil
}

func buildWorkerEnv(opts StartOptions, outputDir, browserPath string) []string {
	env := os.Environ()
	set := map[string]string{
		"PYTHONUTF8":         "1",
		"PYTHONUNBUFFERED":   "1",
		"REGISTER_OUTPUT_DIR": outputDir,
		"REGISTER_LOG_MODE":  "user",
		"EMAIL_MODE":         firstNonEmpty(strings.TrimSpace(opts.EmailMode), "tempmail"),
	}
	if opts.Debug {
		set["REGISTER_LOG_MODE"] = "debug"
	}
	if value := strings.TrimSpace(opts.Proxy); value != "" {
		set["REGISTER_PROXY"] = value
	}
	if value := strings.TrimSpace(opts.EmailAPI); value != "" {
		set["EMAIL_API"] = value
	}
	if value := strings.TrimSpace(opts.EmailDomain); value != "" {
		set["EMAIL_DOMAIN"] = value
	}
	if browserPath != "" {
		set["CLOAKBROWSER_EXECUTABLE_PATH"] = browserPath
	}
	// rebuild env with overrides
	keys := make(map[string]struct{}, len(set))
	for key := range set {
		keys[strings.ToUpper(key)] = struct{}{}
	}
	out := make([]string, 0, len(env)+len(set))
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 0 {
			continue
		}
		if _, ok := keys[strings.ToUpper(parts[0])]; ok {
			continue
		}
		// drop conflicting proxy when REGISTER_PROXY provided
		if _, hasProxy := set["REGISTER_PROXY"]; hasProxy {
			upper := strings.ToUpper(parts[0])
			if upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
				continue
			}
		}
		out = append(out, item)
	}
	for key, value := range set {
		out = append(out, key+"="+value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func probePythonModule(python, enginePath, module string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", "import importlib.util,sys; sys.exit(0 if importlib.util.find_spec(sys.argv[1]) else 1)", module)
	cmd.Dir = enginePath
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

type osProcess struct {
	cmd    *exec.Cmd
	code   int
	waited bool
	mu     sync.Mutex
}

func defaultProcessFactory(ctx context.Context, name string, arg ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, name, arg...)
	if runtime.GOOS == "windows" {
		hideWindowsConsole(cmd)
	}
	return &osProcess{cmd: cmd}, nil
}

func (p *osProcess) Start() error { return p.cmd.Start() }

func (p *osProcess) StdoutPipe() (io.ReadCloser, error) {
	// merge stderr into stdout
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p.cmd.Stderr = p.cmd.Stdout
	return stdout, nil
}

func (p *osProcess) Wait() error {
	err := p.cmd.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waited = true
	if p.cmd.ProcessState != nil {
		p.code = p.cmd.ProcessState.ExitCode()
	} else if err != nil {
		p.code = 1
	}
	return err
}

func (p *osProcess) KillTree() error {
	if p.cmd.Process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		kill := exec.Command("taskkill", "/PID", strconv.Itoa(p.cmd.Process.Pid), "/T", "/F")
		_ = kill.Run()
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *osProcess) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osProcess) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}
