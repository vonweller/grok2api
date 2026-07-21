package windowsregister

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBrowserPathPrefersExplicitThenManagedThenSystem(t *testing.T) {
	root := t.TempDir()
	explicit := touchBrowserFile(t, filepath.Join(root, "explicit.exe"))
	managed := touchBrowserFile(t, filepath.Join(root, "managed", "chrome.exe"))
	system := touchBrowserFile(t, filepath.Join(root, "system", "msedge.exe"))

	if got := resolveBrowserPath(explicit, managed, []string{system}); got != explicit {
		t.Fatalf("explicit candidate = %q, want %q", got, explicit)
	}
	if got := resolveBrowserPath("", managed, []string{system}); got != managed {
		t.Fatalf("managed candidate = %q, want %q", got, managed)
	}
	if got := resolveBrowserPath("", "missing-managed.exe", []string{system}); got != system {
		t.Fatalf("system candidate = %q, want %q", got, system)
	}
}

func TestResolveBrowserPathReturnsEmptyWhenNoCandidateExists(t *testing.T) {
	if got := resolveBrowserPath("", "missing-managed.exe", []string{"missing.exe"}); got != "" {
		t.Fatalf("browser path = %q, want empty", got)
	}
}

func touchBrowserFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("browser"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(abs)
}
