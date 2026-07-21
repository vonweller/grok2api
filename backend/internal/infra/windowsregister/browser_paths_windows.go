//go:build windows

package windowsregister

import (
	"os"
	"path/filepath"
	"strings"
)

func systemBrowserPaths() []string {
	paths := make([]string, 0, 6)
	appendCandidate := func(root string, parts ...string) {
		if root = strings.TrimSpace(root); root != "" {
			paths = append(paths, filepath.Join(append([]string{root}, parts...)...))
		}
	}

	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	localAppData := os.Getenv("LocalAppData")
	appendCandidate(programFiles, "Google", "Chrome", "Application", "chrome.exe")
	appendCandidate(programFilesX86, "Google", "Chrome", "Application", "chrome.exe")
	appendCandidate(localAppData, "Google", "Chrome", "Application", "chrome.exe")
	appendCandidate(programFiles, "Microsoft", "Edge", "Application", "msedge.exe")
	appendCandidate(programFilesX86, "Microsoft", "Edge", "Application", "msedge.exe")
	appendCandidate(localAppData, "Microsoft", "Edge", "Application", "msedge.exe")
	return paths
}
