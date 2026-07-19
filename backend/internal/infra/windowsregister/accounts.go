package windowsregister

import (
	"errors"
	"os"
	"strings"
)

// Record is one registration output line: email:password:sso.
type Record struct {
	Email    string
	Password string
	SSO      string
}

// ReadAccountsFile parses valid registration records, de-duplicating by SSO.
// A missing file is treated as empty rather than an error so status stays usable.
func ReadAccountsFile(path string) ([]Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]Record, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(raw), "\n") {
		record, ok := parseRegistrationRecord(line)
		if !ok {
			continue
		}
		if _, exists := seen[record.SSO]; exists {
			continue
		}
		seen[record.SSO] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

// ScopeRecords returns either the full list or records after baseline.
func ScopeRecords(records []Record, baseline int, scope string) []Record {
	if scope != "current" {
		return append([]Record(nil), records...)
	}
	if baseline < 0 {
		baseline = 0
	}
	if baseline >= len(records) {
		return nil
	}
	return append([]Record(nil), records[baseline:]...)
}

// SSOTokens extracts SSO values in file order.
func SSOTokens(records []Record) []string {
	tokens := make([]string, 0, len(records))
	for _, record := range records {
		if token := strings.TrimSpace(record.SSO); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func parseRegistrationRecord(line string) (Record, bool) {
	parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
	if len(parts) != 3 {
		return Record{}, false
	}
	email := strings.TrimSpace(parts[0])
	password := strings.TrimSpace(parts[1])
	sso := strings.TrimSpace(parts[2])
	if !strings.Contains(email, "@") || password == "" || sso == "" {
		return Record{}, false
	}
	return Record{Email: email, Password: password, SSO: sso}, true
}
