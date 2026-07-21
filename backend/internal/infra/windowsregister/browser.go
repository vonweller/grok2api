package windowsregister

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

var ErrBrowserUnavailable = errors.New("registration browser unavailable")

type BrowserLaunchOptions struct {
	ExecutablePath string
	Proxy          string
}

type BrowserCookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	HTTPOnly bool
	Secure   bool
}

type BrowserFactory interface {
	Launch(context.Context, BrowserLaunchOptions) (Browser, error)
}

type Browser interface {
	NewPage(context.Context) (BrowserPage, error)
	Close() error
}

type BrowserPage interface {
	Navigate(context.Context, string) error
	HTML(context.Context) (string, error)
	Evaluate(context.Context, string, ...any) (json.RawMessage, error)
	Cookies(context.Context) ([]BrowserCookie, error)
	Close() error
}

func authenticationCookie(cookies []BrowserCookie) (string, bool) {
	for _, wanted := range []string{"sso", "sso-rw"} {
		for _, cookie := range cookies {
			if strings.EqualFold(strings.TrimSpace(cookie.Name), wanted) {
				if value := strings.TrimSpace(cookie.Value); value != "" {
					return value, true
				}
			}
		}
	}
	return "", false
}
