package windowsregister

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PlatformSupported reports whether managed registration is available on this OS.
func PlatformSupported() bool {
	return runtime.GOOS == "windows"
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

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
