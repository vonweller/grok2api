package windowsregister

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	signupBaseURL          = "https://accounts.x.ai"
	maxSignupResponseBytes = 1 << 20
	browserFetchScript     = `(args) => fetch(args.url, Object.assign({}, args.init, args.bodyBase64 ? {body: Uint8Array.from(atob(args.bodyBase64), c => c.charCodeAt(0))} : {})).then(async r => { const text = await r.text(); return {status: r.status, retryAfter: r.headers.get('retry-after') || '', grpcStatus: r.headers.get('grpc-status') || '', text: text.slice(0, args.maxTextChars), truncated: text.length > args.maxTextChars}; })`
)

var (
	ErrRateLimited          = errors.New("registration rate limited")
	ErrSignupResponse       = errors.New("registration response invalid")
	ErrSignupRedirect       = errors.New("registration redirect rejected")
	ErrAuthenticationCookie = errors.New("registration authentication cookie missing")
	ErrChallengeRequired    = errors.New("registration challenge required")
	ErrRegistrationRejected = errors.New("registration rejected")
	setCookieURLPattern     = regexp.MustCompile(`https://[^"\s\\]+/set-cookie\?q=[A-Za-z0-9_.%\-]+`)
)

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

type SignupClient struct {
	page   BrowserPage
	config SignupConfig
}

type browserFetchArgs struct {
	URL          string           `json:"url"`
	Init         browserFetchInit `json:"init"`
	BodyBase64   string           `json:"bodyBase64,omitempty"`
	MaxTextChars int              `json:"maxTextChars"`
}

type browserFetchInit struct {
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body,omitempty"`
}

type browserFetchResponse struct {
	Status     int    `json:"status"`
	RetryAfter string `json:"retryAfter"`
	GRPCStatus string `json:"grpcStatus"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated"`
}

func NewSignupClient(page BrowserPage, config SignupConfig) *SignupClient {
	return &SignupClient{page: page, config: config}
}

func (c *SignupClient) SendCode(ctx context.Context, email string) error {
	frame := createEmailValidationFrame(email)
	if frame == nil {
		return ErrSignupResponse
	}
	response, err := c.fetch(ctx, browserFetchArgs{
		URL:        signupBaseURL + "/auth_mgmt.AuthManagement/CreateEmailValidationCode",
		Init:       browserFetchInit{Method: "POST", Headers: grpcWebHeaders()},
		BodyBase64: base64.StdEncoding.EncodeToString(frame),
	})
	if err != nil {
		return err
	}
	return classifyGRPCResponse(response)
}

func (c *SignupClient) VerifyCode(ctx context.Context, email, code string) error {
	frame := verifyEmailValidationFrame(email, code)
	if frame == nil {
		return ErrSignupResponse
	}
	response, err := c.fetch(ctx, browserFetchArgs{
		URL:        signupBaseURL + "/auth_mgmt.AuthManagement/VerifyEmailValidationCode",
		Init:       browserFetchInit{Method: "POST", Headers: grpcWebHeaders()},
		BodyBase64: base64.StdEncoding.EncodeToString(frame),
	})
	if err != nil {
		return err
	}
	return classifyGRPCResponse(response)
}

func (c *SignupClient) Register(ctx context.Context, email, password, code, turnstileToken string) (string, error) {
	payload, err := json.Marshal([]any{map[string]any{
		"emailValidationCode": code,
		"createUserAndSessionRequest": map[string]any{
			"email":              email,
			"givenName":          "James",
			"familyName":         "Smith",
			"clearTextPassword":  password,
			"tosAcceptedVersion": "$undefined",
		},
		"turnstileToken":         turnstileToken,
		"promptOnDuplicateEmail": true,
	}})
	if err != nil {
		return "", ErrSignupResponse
	}
	response, err := c.fetch(ctx, browserFetchArgs{
		URL: signupBaseURL + "/sign-up",
		Init: browserFetchInit{Method: "POST", Headers: map[string]string{
			"accept":                 "text/x-component",
			"content-type":           "text/plain;charset=UTF-8",
			"next-router-state-tree": c.config.StateTree,
			"next-action":            c.config.ActionID,
		}, Body: string(payload)},
	})
	if err != nil {
		return "", err
	}
	if err := classifySignupResponse(response); err != nil {
		return "", err
	}
	redirect, err := registrationRedirect(response.Text)
	if err != nil {
		return "", err
	}
	if err := c.page.Navigate(ctx, redirect); err != nil {
		return "", ErrSignupResponse
	}
	cookies, err := c.page.Cookies(ctx)
	if err != nil {
		return "", ErrSignupResponse
	}
	value, ok := authenticationCookie(cookies)
	if !ok {
		return "", ErrAuthenticationCookie
	}
	return value, nil
}

func (c *SignupClient) fetch(ctx context.Context, args browserFetchArgs) (browserFetchResponse, error) {
	args.MaxTextChars = maxSignupResponseBytes
	raw, err := c.page.Evaluate(ctx, browserFetchScript, args)
	if err != nil {
		if ctx.Err() != nil {
			return browserFetchResponse{}, ctx.Err()
		}
		return browserFetchResponse{}, ErrBrowserCrashed
	}
	if len(raw) > maxSignupResponseBytes+4096 {
		return browserFetchResponse{}, ErrSignupResponse
	}
	var response browserFetchResponse
	if json.Unmarshal(raw, &response) != nil || response.Truncated {
		return browserFetchResponse{}, ErrSignupResponse
	}
	return response, nil
}

func grpcWebHeaders() map[string]string {
	return map[string]string{
		"content-type": "application/grpc-web+proto",
		"x-grpc-web":   "1",
		"x-user-agent": "connect-es/2.1.1",
	}
}

func classifyGRPCResponse(response browserFetchResponse) error {
	if response.Status == httpStatusTooManyRequests || strings.TrimSpace(response.GRPCStatus) == "8" {
		return rateLimitError(response.RetryAfter)
	}
	if containsChallengeMarker(response.Text) {
		return ErrChallengeRequired
	}
	grpcStatus := strings.TrimSpace(response.GRPCStatus)
	if response.Status < 200 || response.Status >= 300 || (grpcStatus != "" && grpcStatus != "0") {
		return ErrSignupResponse
	}
	return nil
}

func classifySignupResponse(response browserFetchResponse) error {
	if response.Status == httpStatusTooManyRequests {
		return rateLimitError(response.RetryAfter)
	}
	if containsChallengeMarker(response.Text) {
		return ErrChallengeRequired
	}
	if response.Status < 200 || response.Status >= 300 {
		return ErrRegistrationRejected
	}
	return nil
}

const httpStatusTooManyRequests = 429

func rateLimitError(retryAfter string) error {
	seconds, _ := strconv.Atoi(strings.TrimSpace(retryAfter))
	if seconds < 0 || seconds > 24*60*60 {
		seconds = 0
	}
	return &RateLimitError{RetryAfter: time.Duration(seconds) * time.Second}
}

func containsChallengeMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"cf-chl", "challenge-platform", "turnstile", "captcha"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func registrationRedirect(responseText string) (string, error) {
	normalized := strings.ReplaceAll(responseText, `\/`, "/")
	rawURL := setCookieURLPattern.FindString(normalized)
	if rawURL == "" {
		return "", ErrRegistrationRejected
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return "", ErrSignupRedirect
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return "", ErrSignupRedirect
	}
	return parsed.String(), nil
}
