package windowsregister

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSignupConfig(t *testing.T) {
	html := readDiscoveryFixture(t, "signup_page.html")
	cfg, err := discoverSignupConfig(html, map[string]string{
		"/_next/static/app.js": `const action="0123456789abcdef0123456789abcdef01234567"; createUser();`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SiteKey != "0x4AAAAAAAtest_site_key" {
		t.Fatalf("site key = %q", cfg.SiteKey)
	}
	if cfg.ActionID != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("action ID = %q", cfg.ActionID)
	}
	stateTree, err := url.QueryUnescape(cfg.StateTree)
	if err != nil {
		t.Fatal(err)
	}
	if stateTree != `[["",{"children":["sign-up",{}]}]],` {
		t.Fatalf("state tree = %q", stateTree)
	}
}

func TestDiscoverSignupConfigRejectsIncompleteOrOversizedInput(t *testing.T) {
	_, err := discoverSignupConfig(`<script src="/_next/static/app.js"></script>`, map[string]string{
		"/_next/static/app.js": `createUser();`,
	})
	if !errors.Is(err, ErrConfigDiscovery) {
		t.Fatalf("incomplete error = %v", err)
	}

	_, err = discoverSignupConfig(strings.Repeat("x", maxDiscoveryDocumentBytes+1), nil)
	if !errors.Is(err, ErrConfigDiscovery) {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestDiscoverSignupConfigInspectsAtMostFiftyAssets(t *testing.T) {
	var html strings.Builder
	assets := make(map[string]string)
	for i := 0; i < maxDiscoveryAssets+1; i++ {
		path := fmt.Sprintf("/_next/static/chunk-%02d.js", i)
		html.WriteString(`<script src="` + path + `"></script>`)
		assets[path] = "noop"
	}
	html.WriteString(`<div data-sitekey="0x4AAAAAAAtest_site_key"></div>`)
	html.WriteString(`<script>self.__next_f.push([1,"sign-up \"f\":[[[\"\",{\"children\":[\"sign-up\",{}]}]],\"$undefined\""])</script>`)
	last := fmt.Sprintf("/_next/static/chunk-%02d.js", maxDiscoveryAssets)
	assets[last] = `createUser(); const action="0123456789abcdef0123456789abcdef01234567";`

	_, err := discoverSignupConfig(html.String(), assets)
	if !errors.Is(err, ErrConfigDiscovery) {
		t.Fatalf("asset limit error = %v", err)
	}
}

func readDiscoveryFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
