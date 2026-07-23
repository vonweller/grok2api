package windowsregister_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

type fakeProcess struct {
	mu       sync.Mutex
	lines    []string
	exitCode int
	started  chan struct{}
	done     chan struct{}
	killed   bool
	waitErr  error
	reader   *io.PipeReader
	writer   *io.PipeWriter
}

func newFakeProcess(lines []string, exitCode int) *fakeProcess {
	r, w := io.Pipe()
	return &fakeProcess{
		lines:    lines,
		exitCode: exitCode,
		started:  make(chan struct{}),
		done:     make(chan struct{}),
		reader:   r,
		writer:   w,
	}
}

func (p *fakeProcess) Start() error {
	close(p.started)
	go func() {
		for _, line := range p.lines {
			_, _ = io.WriteString(p.writer, line+"\n")
		}
		_ = p.writer.Close()
		close(p.done)
	}()
	return nil
}

func (p *fakeProcess) StdoutPipe() (io.ReadCloser, error) { return p.reader, nil }

func (p *fakeProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitErr != nil {
		return p.waitErr
	}
	if p.exitCode != 0 {
		return errors.New("exit status 1")
	}
	return nil
}

func (p *fakeProcess) KillTree() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	_ = p.writer.Close()
	return nil
}

func (p *fakeProcess) Pid() int { return 4242 }

func (p *fakeProcess) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode
}

func TestStartRejectsWhenRunning(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	if err := os.MkdirAll(filepath.Join(engine, "grok_register"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine, "grok_register", "register.py"), []byte("#"), 0o644); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	svc := windowsregister.NewService(windowsregister.Config{
		Enabled:    true,
		EnginePath: engine,
		OutputDir:  filepath.Join(dir, "out"),
		PythonPath: "python-fake",
	})
	svc.SetPlatformSupported(true)
	svc.SetProcessFactory(func(ctx context.Context, name string, arg ...string) (windowsregister.Process, error) {
		p := newFakeProcess(nil, 0)
		// override Start to not auto-close; stop via block/KillTree
		return &blockingProcess{fakeProcess: p, block: block}, nil
	})
	// readiness: engine present + python path set is enough when probes disabled
	svc.SetSkipRuntimeProbes(true)

	if _, err := svc.Start(windowsregister.StartOptions{Target: 1, EmailMode: "tempmail"}); err != nil {
		t.Fatalf("first start: %v", err)
	}
	_, err := svc.Start(windowsregister.StartOptions{Target: 1, EmailMode: "tempmail"})
	if !errors.Is(err, windowsregister.ErrAlreadyRunning) {
		t.Fatalf("expected already running, got %v", err)
	}
	close(block)
	_, _ = svc.Stop(context.Background())
}

type blockingProcess struct {
	*fakeProcess
	block chan struct{}
	once  sync.Once
}

func (p *blockingProcess) Start() error {
	close(p.started)
	return nil
}

func (p *blockingProcess) Wait() error {
	<-p.block
	p.once.Do(func() {
		_ = p.writer.Close()
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	})
	return nil
}

func (p *blockingProcess) KillTree() error {
	p.once.Do(func() {
		_ = p.writer.Close()
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	})
	return p.fakeProcess.KillTree()
}

func TestStatusCountsFromLogsAndAccounts(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	if err := os.MkdirAll(filepath.Join(engine, "grok_register"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine, "grok_register", "register.py"), []byte("#"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(out, "accounts.txt")
	if err := os.WriteFile(accounts, []byte("a@x.com:pw:sso1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := windowsregister.NewService(windowsregister.Config{
		Enabled:    true,
		EnginePath: engine,
		OutputDir:  out,
		PythonPath: "python-fake",
	})
	svc.SetPlatformSupported(true)
	svc.SetSkipRuntimeProbes(true)
	svc.SetProcessFactory(func(ctx context.Context, name string, arg ...string) (windowsregister.Process, error) {
		return newFakeProcess([]string{
			"[✓] 注册成功 #2",
			"registration failed once",
			"触发限流",
		}, 0), nil
	})

	if _, err := svc.Start(windowsregister.StartOptions{Target: 2, EmailMode: "tempmail"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// append one more account during run
	if err := os.WriteFile(accounts, []byte("a@x.com:pw:sso1\nb@x.com:pw:sso2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var status windowsregister.Status
	for time.Now().Before(deadline) {
		status = svc.Status()
		if status.State == windowsregister.StateCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.State != windowsregister.StateCompleted {
		t.Fatalf("state=%s logs=%v", status.State, status.Logs)
	}
	if status.GeneratedThisRun != 1 {
		t.Fatalf("generatedThisRun=%d", status.GeneratedThisRun)
	}
	if status.Success < 1 {
		t.Fatalf("success=%d", status.Success)
	}
	if status.Failed < 1 || status.RateLimited < 1 {
		t.Fatalf("failed=%d rateLimited=%d", status.Failed, status.RateLimited)
	}
	joined := strings.Join(status.Logs, "\n")
	if !strings.Contains(joined, "注册成功") {
		t.Fatalf("logs missing success: %s", joined)
	}
}

func TestNonWindowsNotSupported(t *testing.T) {
	svc := windowsregister.NewService(windowsregister.Config{Enabled: true, EnginePath: t.TempDir(), OutputDir: t.TempDir()})
	svc.SetPlatformSupported(false)
	_, err := svc.Start(windowsregister.StartOptions{Target: 1, EmailMode: "tempmail"})
	if !errors.Is(err, windowsregister.ErrPlatformUnsupported) {
		t.Fatalf("got %v", err)
	}
	status := svc.Status()
	if status.PlatformSupported || status.Ready {
		t.Fatalf("status=%+v", status)
	}
}

func TestImportTokensScope(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	if err := os.MkdirAll(filepath.Join(engine, "grok_register"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine, "grok_register", "register.py"), []byte("#"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, "accounts.txt")
	if err := os.WriteFile(path, []byte("a@x.com:pw:sso1\nb@x.com:pw:sso2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := windowsregister.NewService(windowsregister.Config{Enabled: true, OutputDir: out, EnginePath: engine, PythonPath: "python-fake"})
	svc.SetPlatformSupported(true)
	svc.SetSkipRuntimeProbes(true)
	// simulate a prior run baseline of 1 via Start bookkeeping without process? set via Start with instant process
	svc.SetProcessFactory(func(ctx context.Context, name string, arg ...string) (windowsregister.Process, error) {
		return newFakeProcess(nil, 0), nil
	})
	if _, err := svc.Start(windowsregister.StartOptions{Target: 1, EmailMode: "tempmail"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !svc.Status().Running && svc.Status().State != windowsregister.StateStarting && svc.Status().State != windowsregister.StateRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// baseline was 2 at start, so current empty unless we append
	if err := os.WriteFile(path, []byte("a@x.com:pw:sso1\nb@x.com:pw:sso2\nc@x.com:pw:sso3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := svc.ImportTokens("current")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0] != "sso3" {
		t.Fatalf("tokens=%v", tokens)
	}
	all, err := svc.ImportTokens("all")
	if err != nil || len(all) != 3 {
		t.Fatalf("all=%v err=%v", all, err)
	}
}
