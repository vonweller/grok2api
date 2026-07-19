package windowsregister_test

import (
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

func TestSanitizeLogHidesSecrets(t *testing.T) {
	in := "sso=eyJhbGciOiJIUzI1NiJ9.aaa.bbb password=secret user@example.com http://u:p@127.0.0.1:7890"
	out := windowsregister.SanitizeLog(in)
	if strings.Contains(out, "eyJ") || strings.Contains(out, "secret") || strings.Contains(out, "user@") || strings.Contains(out, "u:p@") {
		t.Fatalf("secrets leaked: %q", out)
	}
	if !strings.Contains(out, "[token hidden]") && !strings.Contains(out, "[hidden]") {
		t.Fatalf("expected redaction markers: %q", out)
	}
}

func TestSanitizeLogTruncates(t *testing.T) {
	in := strings.Repeat("a", 3000)
	out := windowsregister.SanitizeLog(in)
	if len(out) > 2000 {
		t.Fatalf("expected truncation, got %d", len(out))
	}
}

func TestClassifyLogLine(t *testing.T) {
	cases := []struct {
		line                          string
		success, failed, rateLimited  bool
	}{
		{"[✓] 注册成功 #1", true, false, false},
		{"registration succeeded", true, false, false},
		{"[x] 注册失败", false, true, false},
		{"registration failed", false, true, false},
		{"触发限流 | 60秒后恢复", false, false, true},
		{"rate limit hit", false, false, true},
		{"ordinary log", false, false, false},
	}
	for _, tc := range cases {
		success, failed, rateLimited := windowsregister.ClassifyLogLine(tc.line)
		if success != tc.success || failed != tc.failed || rateLimited != tc.rateLimited {
			t.Fatalf("line %q => (%v,%v,%v), want (%v,%v,%v)", tc.line, success, failed, rateLimited, tc.success, tc.failed, tc.rateLimited)
		}
	}
}
