package windowsregister

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ansiRe            = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	jwtRe             = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{12,}(?:\.[A-Za-z0-9_-]{8,}){1,2}\b`)
	secretRe          = regexp.MustCompile(`(?i)\b(sso|password|passwd|cookie|authorization|token)\b\s*[:=]\s*([^\s,;]+)`)
	proxyCredentialRe = regexp.MustCompile(`(://)([^\s:/@]+):([^\s/@]+)@`)
	// RE2 has no lookbehind; match an optional non-local-part prefix and keep it.
	emailRe = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9._+-])([A-Za-z0-9._+-])([A-Za-z0-9._+-]*)(@[A-Za-z0-9.-]+\.[A-Za-z]{2,})`)
)

// SanitizeLog redacts credentials and bounds a registration worker log line.
func SanitizeLog(value string) string {
	text := ansiRe.ReplaceAllString(value, "")
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.TrimSpace(text)
	text = jwtRe.ReplaceAllString(text, "[token hidden]")
	text = secretRe.ReplaceAllString(text, "${1}=[hidden]")
	text = proxyCredentialRe.ReplaceAllString(text, "${1}***:***@")
	text = emailRe.ReplaceAllString(text, "${1}${2}***${4}")
	if utf8.RuneCountInString(text) > 2000 {
		runes := []rune(text)
		text = string(runes[:2000])
	}
	return text
}

// ClassifyLogLine reports coarse counters derived from sanitized worker output.
func ClassifyLogLine(line string) (success, failed, rateLimited bool) {
	lower := strings.ToLower(line)
	if strings.Contains(line, "注册成功") || strings.Contains(lower, "registration succeeded") {
		success = true
	}
	if strings.Contains(line, "注册失败") || strings.Contains(lower, "registration failed") {
		failed = true
	}
	if strings.Contains(line, "触发限流") || strings.Contains(lower, "rate limit") {
		rateLimited = true
	}
	return success, failed, rateLimited
}
