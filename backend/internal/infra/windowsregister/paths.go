package windowsregister

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PlatformSupported reports whether managed registration is available on this OS.
func PlatformSupported() bool {
	return runtime.GOOS == "windows"
}

func resolvePython(configured, enginePath string) string {
	// Prefer the package-local virtualenv before any host interpreter. ZIP packages
	// never ship site-packages; deploy.ps1 creates tools/windows-register/.venv and
	// installs playwright/cloakbrowser there. Falling back to a bare host python
	// makes the admin UI report "missing: playwright, cloakbrowser" even after setup.
	venvPython := filepath.Join(enginePath, ".venv", "Scripts", "python.exe")
	if fileExists(venvPython) {
		return venvPython
	}
	venvPythonUnix := filepath.Join(enginePath, ".venv", "bin", "python")
	if fileExists(venvPythonUnix) {
		return venvPythonUnix
	}
	if value := strings.TrimSpace(configured); value != "" {
		if fileExists(value) {
			return value
		}
		if path, err := exec.LookPath(value); err == nil {
			return path
		}
	}
	if env := strings.TrimSpace(os.Getenv("GROK2API_REGISTER_PYTHON")); env != "" {
		if fileExists(env) {
			return env
		}
		if path, err := exec.LookPath(env); err == nil {
			return path
		}
	}
	for _, candidate := range []string{"py", "python", "python3"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func resolveBrowserPath(configured, managed string, system []string) string {
	candidates := append([]string{strings.TrimSpace(configured), strings.TrimSpace(managed)}, system...)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || !fileExists(candidate) {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err == nil {
			return filepath.Clean(absolute)
		}
	}
	return ""
}

// resolveLegacyBrowserPath keeps the subprocess worker functional until the
// native runner replaces it. It is removed with the Python runtime.
func resolveLegacyBrowserPath(enginePath string) string {
	for _, key := range []string{"CLOAKBROWSER_EXECUTABLE_PATH", "XAI_ENROLLER_BROWSER_EXECUTABLE"} {
		if value := strings.TrimSpace(strings.Trim(os.Getenv(key), `"`)); value != "" {
			value = os.ExpandEnv(value)
			if fileExists(value) {
				return value
			}
		}
	}
	// setup.ps1 / deploy.ps1 can pin the discovered browser for service accounts that do not
	// share the interactive user's profile paths.
	if enginePath != "" {
		if marker := strings.TrimSpace(readFirstLine(filepath.Join(enginePath, ".browser-path"))); marker != "" && fileExists(marker) {
			return marker
		}
	}
	var roots []string
	// deploy.ps1 installs Chromium under the package tree so scheduled tasks and
	// plain exe launches can both resolve it without the interactive profile.
	if enginePath != "" {
		roots = append(roots,
			filepath.Join(enginePath, ".cloakbrowser"),
			filepath.Join(enginePath, "browser"),
			filepath.Join(enginePath, "AppData", "Local", "cloakbrowser"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, ".cloakbrowser"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "cloakbrowser"))
	}
	// LOCAL SERVICE / other service accounts cannot see the interactive user's
	// profile through UserHomeDir. Scan common Windows profile locations.
	roots = append(roots, discoverCloakBrowserRoots()...)
	var newest string
	var newestMod int64
	seen := make(map[string]struct{})
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(root)]; ok {
			continue
		}
		seen[strings.ToLower(root)] = struct{}{}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			name := strings.ToLower(d.Name())
			if name != "chrome.exe" && name != "chromium" && name != "chrome" {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			mod := info.ModTime().UnixNano()
			if newest == "" || mod > newestMod {
				newest = path
				newestMod = mod
			}
			return nil
		})
	}
	return newest
}

func discoverCloakBrowserRoots() []string {
	roots := make([]string, 0, 8)
	// Typical interactive installs.
	if entries, err := os.ReadDir(`C:\Users`); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			// skip system profile dirs
			lower := strings.ToLower(name)
			if lower == "public" || lower == "default" || lower == "default user" || lower == "all users" {
				continue
			}
			roots = append(roots,
				filepath.Join(`C:\Users`, name, ".cloakbrowser"),
				filepath.Join(`C:\Users`, name, "AppData", "Local", "cloakbrowser"),
			)
		}
	}
	return roots
}

func readFirstLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(raw))
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return strings.Trim(line, `"'`)
}

func enginePresent(enginePath string) bool {
	return fileExists(filepath.Join(enginePath, "grok_register", "register.py"))
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
