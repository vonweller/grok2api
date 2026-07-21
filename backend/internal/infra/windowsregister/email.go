package windowsregister

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	maxEmailResponseBytes = 1 << 20
	emailRequestTimeout   = 20 * time.Second
	defaultPollInterval   = 2 * time.Second
	defaultPollTimeout    = 90 * time.Second
)

var (
	ErrEmailProviderUnavailable = errors.New("email provider unavailable")
	ErrEmailResponse            = errors.New("email provider response invalid")
	ErrEmailTimeout             = errors.New("email verification code timeout")
	ErrEmailDomain              = errors.New("email domain mismatch")

	emailCodePatterns = []*regexp.Regexp{
		regexp.MustCompile(`>([A-Z0-9]{3}-[A-Z0-9]{3})<`),
		regexp.MustCompile(`>([A-Z0-9]{6})<`),
		regexp.MustCompile(`\b([A-Z0-9]{3}-?[A-Z0-9]{3})\b`),
	}
	exactEmailCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{3}-?[A-Za-z0-9]{3}$`)
)

type Mailbox struct {
	Address  string
	Password string
	Handle   json.RawMessage
}

type EmailProvider interface {
	Create(context.Context) (Mailbox, error)
	PollCode(context.Context, Mailbox) (string, error)
}

func NewEmailProvider(mode, api, domain string, client *http.Client) (EmailProvider, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "tempmail":
		return NewTempMailProvider(client), nil
	case "custom":
		return NewCustomEmailProvider(api, domain, client), nil
	default:
		return nil, ErrEmailProviderUnavailable
	}
}

func extractEmailCode(value string) string {
	for _, pattern := range emailCodePatterns {
		match := pattern.FindStringSubmatch(value)
		if len(match) == 2 {
			return strings.ReplaceAll(match[1], "-", "")
		}
	}
	return ""
}

func normalizeEmailCode(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !exactEmailCodePattern.MatchString(value) {
		return "", false
	}
	return strings.ToUpper(strings.ReplaceAll(value, "-", "")), true
}

func configuredHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: emailRequestTimeout}
	}
	clone := *client
	if clone.Timeout <= 0 || clone.Timeout > emailRequestTimeout {
		clone.Timeout = emailRequestTimeout
	}
	return &clone
}

func readEmailJSON(ctx context.Context, client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmailResponse
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ErrEmailResponse
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxEmailResponseBytes+1))
	if err != nil || len(raw) > maxEmailResponseBytes || json.Unmarshal(raw, target) != nil {
		return ErrEmailResponse
	}
	return nil
}

func checkEmailStatus(ctx context.Context, client *http.Client, request *http.Request) error {
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEmailResponse
	}
	defer response.Body.Close()
	readBytes, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxEmailResponseBytes+1))
	if readErr != nil || readBytes > maxEmailResponseBytes {
		return ErrEmailResponse
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ErrEmailResponse
	}
	return nil
}

func pollEmailCode(ctx context.Context, interval, timeout time.Duration, fetch func(context.Context) (string, error)) (string, error) {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if timeout <= 0 {
		timeout = defaultPollTimeout
	}
	pollContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		code, err := fetch(pollContext)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if pollContext.Err() != nil {
				return "", ErrEmailTimeout
			}
			return "", err
		}
		if code != "" {
			normalized, ok := normalizeEmailCode(code)
			if !ok {
				return "", ErrEmailResponse
			}
			return normalized, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-pollContext.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", ErrEmailTimeout
		case <-timer.C:
		}
	}
}

func normalizeEmailBase(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrEmailProviderUnavailable
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func randomMailboxAddress(domain string) (string, error) {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" || strings.ContainsAny(domain, "@/:\\ \t\r\n") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrEmailDomain
	}
	random := make([]byte, 5)
	if _, err := cryptorand.Read(random); err != nil {
		return "", ErrEmailProviderUnavailable
	}
	return "oc" + hex.EncodeToString(random) + "@" + domain, nil
}

func randomMailboxPassword() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, 15)
	buffer := make([]byte, 32)
	for offset := 0; offset < len(result); {
		if _, err := cryptorand.Read(buffer); err != nil {
			return "", ErrEmailProviderUnavailable
		}
		for _, value := range buffer {
			if value >= 252 {
				continue
			}
			result[offset] = alphabet[int(value)%len(alphabet)]
			offset++
			if offset == len(result) {
				break
			}
		}
	}
	return string(result), nil
}

func mailboxDomainMatches(address, domain string) bool {
	address = strings.TrimSpace(address)
	separator := strings.LastIndexByte(address, '@')
	return separator > 0 && separator < len(address)-1 && strings.EqualFold(address[separator+1:], strings.TrimSpace(domain))
}
