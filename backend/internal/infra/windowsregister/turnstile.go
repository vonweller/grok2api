package windowsregister

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	injectTurnstileScript = `(args) => { let container = document.querySelector('.cf-turnstile'); if (!container) { container = document.createElement('div'); container.className = 'cf-turnstile'; document.body.appendChild(container); } const writeToken = token => { let input = document.querySelector('input[name="cf-turnstile-response"]'); if (!input) { input = document.createElement('input'); input.type = 'hidden'; input.name = 'cf-turnstile-response'; document.body.appendChild(input); } input.value = token || ''; }; const render = () => window.turnstile && window.turnstile.render(container, {sitekey: args.siteKey, callback: writeToken}); if (window.turnstile) { render(); return; } if (!document.querySelector('script[data-grok2api-turnstile]')) { const script = document.createElement('script'); script.dataset.grok2apiTurnstile = '1'; script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js'; script.onload = render; document.head.appendChild(script); } }`
	readTurnstileScript   = `() => document.querySelector('input[name="cf-turnstile-response"]')?.value || ''`
	turnstileCenterScript = `() => { const element = document.querySelector('.cf-turnstile'); if (!element) return null; const rect = element.getBoundingClientRect(); if (rect.width < 20 || rect.height < 20) return null; return {x: rect.left + rect.width / 2, y: rect.top + rect.height / 2}; }`
)

var (
	ErrTurnstileTimeout = errors.New("turnstile timeout")
	ErrTurnstileFailed  = errors.New("turnstile failed")
)

type TurnstileSolver struct {
	PollInterval time.Duration
	HardTimeout  time.Duration
}

type browserPageClicker interface {
	Click(context.Context, float64, float64) error
}

func (s TurnstileSolver) Solve(ctx context.Context, page BrowserPage, siteKey string) (string, error) {
	defer page.Close()
	if !strings.HasPrefix(siteKey, "0x4") || len(siteKey) > 256 {
		return "", ErrTurnstileFailed
	}
	interval := s.PollInterval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	timeout := s.HardTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	solveContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := page.Evaluate(solveContext, injectTurnstileScript, map[string]string{"siteKey": siteKey}); err != nil {
		return "", turnstileContextError(ctx, solveContext)
	}
	for {
		raw, err := page.Evaluate(solveContext, readTurnstileScript)
		if err != nil {
			return "", turnstileContextError(ctx, solveContext)
		}
		var token string
		if json.Unmarshal(raw, &token) != nil || len(token) > 4096 {
			return "", ErrTurnstileFailed
		}
		if token = strings.TrimSpace(token); token != "" {
			return token, nil
		}
		if clicker, ok := page.(browserPageClicker); ok {
			rawCenter, centerErr := page.Evaluate(solveContext, turnstileCenterScript)
			if centerErr == nil {
				var center *struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
				}
				if json.Unmarshal(rawCenter, &center) == nil && center != nil {
					_ = clicker.Click(solveContext, center.X, center.Y)
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-solveContext.Done():
			timer.Stop()
			return "", turnstileContextError(ctx, solveContext)
		case <-timer.C:
		}
	}
}

func turnstileContextError(parent, child context.Context) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if child.Err() != nil {
		return ErrTurnstileTimeout
	}
	return ErrTurnstileFailed
}
