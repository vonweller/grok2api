package windowsregister

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const browserCloseTimeout = 5 * time.Second

type rodBrowserFactory struct{}

type rodBrowser struct {
	browser  *rod.Browser
	launcher *launcher.Launcher
	close    sync.Once
	closeErr error
}

type rodPage struct {
	page  *rod.Page
	close sync.Once
	err   error
}

func NewRodBrowserFactory() BrowserFactory {
	return rodBrowserFactory{}
}

func newRodLauncher(options BrowserLaunchOptions) (*launcher.Launcher, error) {
	executable := strings.TrimSpace(options.ExecutablePath)
	if !fileExists(executable) {
		return nil, ErrBrowserUnavailable
	}
	l := launcher.New().Bin(executable).Headless(true).Leakless(false)
	if proxy := strings.TrimSpace(options.Proxy); proxy != "" {
		l.Proxy(proxy)
	}
	return l, nil
}

func (rodBrowserFactory) Launch(ctx context.Context, options BrowserLaunchOptions) (Browser, error) {
	l, err := newRodLauncher(options)
	if err != nil {
		return nil, err
	}
	l = l.Context(ctx)
	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("%w: launch failed", ErrBrowserUnavailable)
	}

	browser := rod.New().ControlURL(controlURL).Context(ctx)
	if err := browser.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		return nil, fmt.Errorf("%w: CDP connection failed", ErrBrowserUnavailable)
	}
	return &rodBrowser{browser: browser, launcher: l}, nil
}

func (b *rodBrowser) NewPage(ctx context.Context) (BrowserPage, error) {
	page, err := b.browser.Context(ctx).Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, err
	}
	return &rodPage{page: page}, nil
}

func (b *rodBrowser) Close() error {
	b.close.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), browserCloseTimeout)
		defer cancel()
		b.closeErr = b.browser.Context(ctx).Close()

		cleaned := make(chan struct{})
		go func() {
			b.launcher.Cleanup()
			close(cleaned)
		}()
		select {
		case <-cleaned:
		case <-ctx.Done():
			b.launcher.Kill()
		}
	})
	return b.closeErr
}

func (p *rodPage) Navigate(ctx context.Context, rawURL string) error {
	return p.page.Context(ctx).Navigate(rawURL)
}

func (p *rodPage) HTML(ctx context.Context) (string, error) {
	return p.page.Context(ctx).HTML()
}

func (p *rodPage) Evaluate(ctx context.Context, expression string, args ...any) (json.RawMessage, error) {
	result, err := p.page.Context(ctx).Eval(expression, args...)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result.Value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (p *rodPage) Cookies(ctx context.Context) ([]BrowserCookie, error) {
	cookies, err := p.page.Context(ctx).Cookies(nil)
	if err != nil {
		return nil, err
	}
	result := make([]BrowserCookie, 0, len(cookies))
	for _, cookie := range cookies {
		result = append(result, BrowserCookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			HTTPOnly: cookie.HTTPOnly,
			Secure:   cookie.Secure,
		})
	}
	return result, nil
}

func (p *rodPage) Click(ctx context.Context, x, y float64) error {
	page := p.page.Context(ctx)
	for _, event := range []proto.InputDispatchMouseEvent{
		{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: x, Y: y},
		{Type: proto.InputDispatchMouseEventTypeMousePressed, X: x, Y: y, Button: proto.InputMouseButtonLeft, ClickCount: 1},
		{Type: proto.InputDispatchMouseEventTypeMouseReleased, X: x, Y: y, Button: proto.InputMouseButtonLeft, ClickCount: 1},
	} {
		if err := event.Call(page); err != nil {
			return err
		}
	}
	return nil
}

func (p *rodPage) Close() error {
	p.close.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), browserCloseTimeout)
		defer cancel()
		p.err = p.page.Context(ctx).Close()
	})
	return p.err
}
