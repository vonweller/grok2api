package windowsregister_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

func TestReadRegistrationRecordsAndScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.txt")
	content := "a@x.com:pw1:sso1\nbad\nc@x.com:pw2:sso2\na@x.com:pw1:sso1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := windowsregister.ReadAccountsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records: %+v", len(records), records)
	}
	current := windowsregister.ScopeRecords(records, 1, "current")
	if len(current) != 1 || current[0].SSO != "sso2" {
		t.Fatalf("scope current failed: %+v", current)
	}
	all := windowsregister.ScopeRecords(records, 1, "all")
	if len(all) != 2 {
		t.Fatalf("scope all failed: %+v", all)
	}
	tokens := windowsregister.SSOTokens(current)
	if len(tokens) != 1 || tokens[0] != "sso2" {
		t.Fatalf("tokens=%v", tokens)
	}
}

func TestReadAccountsFileMissing(t *testing.T) {
	records, err := windowsregister.ReadAccountsFile(filepath.Join(t.TempDir(), "missing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected empty, got %+v", records)
	}
}
