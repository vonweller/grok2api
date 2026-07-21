package windowsregister

import (
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
)

const (
	maxDiscoveryDocumentBytes = 8 << 20
	maxDiscoveryAssetBytes    = 8 << 20
	maxDiscoveryAssets        = 50
)

var (
	ErrConfigDiscovery  = errors.New("registration config discovery failed")
	siteKeyPattern      = regexp.MustCompile(`0x4AAAAAAA[A-Za-z0-9_-]+`)
	scriptSourcePattern = regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*["'](/_next/static/[^"']+\.js(?:\?[^"']*)?)["']`)
	actionIDPattern     = regexp.MustCompile(`\b[0-9a-fA-F]{40,50}\b`)
	flightPushPattern   = regexp.MustCompile(`self\.__next_f\.push\(\[1,\s*`)
)

type SignupConfig struct {
	SiteKey   string
	ActionID  string
	StateTree string
}

func discoverSignupConfig(document string, assets map[string]string) (SignupConfig, error) {
	if len(document) == 0 || len(document) > maxDiscoveryDocumentBytes {
		return SignupConfig{}, ErrConfigDiscovery
	}
	document = html.UnescapeString(document)
	cfg := SignupConfig{
		SiteKey:   siteKeyPattern.FindString(document),
		StateTree: discoverStateTree(document),
	}

	sources := scriptSourcePattern.FindAllStringSubmatch(document, maxDiscoveryAssets)
	for _, source := range sources {
		if len(source) < 2 {
			continue
		}
		asset, ok := assets[source[1]]
		if !ok || len(asset) == 0 || len(asset) > maxDiscoveryAssetBytes {
			continue
		}
		lower := strings.ToLower(asset)
		if !strings.Contains(lower, "createuser") && !strings.Contains(lower, "registeruser") && !strings.Contains(lower, "emailvalidation") {
			continue
		}
		if actionID := actionIDPattern.FindString(asset); actionID != "" {
			cfg.ActionID = actionID
			break
		}
	}

	if cfg.SiteKey == "" || cfg.ActionID == "" || cfg.StateTree == "" {
		return SignupConfig{}, ErrConfigDiscovery
	}
	return cfg, nil
}

func discoverStateTree(document string) string {
	for _, location := range flightPushPattern.FindAllStringIndex(document, -1) {
		chunk, ok := decodeJSONStringPrefix(document[location[1]:])
		if !ok || !strings.Contains(chunk, "sign-up") {
			continue
		}
		const fieldMarker = `"f":[`
		start := strings.Index(chunk, fieldMarker)
		if start < 0 {
			continue
		}
		start += len(fieldMarker)
		end := strings.Index(chunk[start:], `"$undefined"`)
		if end < 0 {
			continue
		}
		stateTree := chunk[start : start+end]
		stateTree = strings.ReplaceAll(stateTree, `\"`, `"`)
		stateTree = strings.ReplaceAll(stateTree, `\`, ``)
		if stateTree == "" {
			continue
		}
		return strings.ReplaceAll(url.QueryEscape(stateTree), "+", "%20")
	}
	return ""
}

func decodeJSONStringPrefix(value string) (string, bool) {
	value = strings.TrimLeft(value, " \t\r\n")
	if len(value) < 2 || value[0] != '"' {
		return "", false
	}
	escaped := false
	for i := 1; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case value[i] == '\\':
			escaped = true
		case value[i] == '"':
			var decoded string
			if err := json.Unmarshal([]byte(value[:i+1]), &decoded); err != nil {
				return "", false
			}
			return decoded, true
		}
	}
	return "", false
}
