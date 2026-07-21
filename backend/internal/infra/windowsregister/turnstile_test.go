package windowsregister

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTurnstileSolverReturnsTokenAndClosesPage(t *testing.T) {
	page := &scriptedBrowserPage{results: []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`""`),
		json.RawMessage(`{"x":150,"y":45}`),
		json.RawMessage(`"token-123"`),
	}}
	solver := TurnstileSolver{PollInterval: time.Millisecond, HardTimeout: time.Second}
	token, err := solver.Solve(t.Context(), page, "0x4AAAAAAAtest")
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-123" || !page.closed || page.clicks != 1 {
		t.Fatalf("token = %q closed = %v clicks = %d", token, page.closed, page.clicks)
	}
	if strings.Contains(page.expressions[0], "0x4AAAAAAAtest") {
		t.Fatal("site key was interpolated into JavaScript")
	}
}

func TestTurnstileSolverTimesOutAndClosesPage(t *testing.T) {
	page := &scriptedBrowserPage{results: []json.RawMessage{json.RawMessage(`null`)}}
	solver := TurnstileSolver{PollInterval: time.Millisecond, HardTimeout: 10 * time.Millisecond}
	_, err := solver.Solve(t.Context(), page, "0x4AAAAAAAtest")
	if !errors.Is(err, ErrTurnstileTimeout) || !page.closed {
		t.Fatalf("error = %v closed = %v", err, page.closed)
	}
}
