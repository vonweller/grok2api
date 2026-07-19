package windowsregister

import (
	"context"
	"fmt"
	"strings"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	windowsregisterinfra "github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

// Worker is the managed registration process surface used by the application layer.
type Worker interface {
	Status() windowsregisterinfra.Status
	Start(opts windowsregisterinfra.StartOptions) (windowsregisterinfra.Status, error)
	Stop(ctx context.Context) (windowsregisterinfra.Status, error)
	ImportTokens(scope string) ([]string, error)
}

// AccountImporter imports SSO documents into existing account pools.
type AccountImporter interface {
	ImportWebCredentials(ctx context.Context, data []byte) (accountapp.ImportResult, error)
	ImportConsoleCredentials(ctx context.Context, data []byte) (accountapp.ImportResult, error)
}

// Service orchestrates worker lifecycle and account import.
type Service struct {
	worker   Worker
	accounts AccountImporter
}

// ImportRequest selects which registration results to import and where.
type ImportRequest struct {
	Scope        string
	Destinations []string
}

// ProviderImportResult is one destination pool import outcome.
type ProviderImportResult struct {
	Provider string `json:"provider"`
	Created  int    `json:"created"`
	Updated  int    `json:"updated"`
	Skipped  int    `json:"skipped"`
	Error    string `json:"error,omitempty"`
}

// ImportResponse summarizes a multi-destination import.
type ImportResponse struct {
	Scope       string                 `json:"scope"`
	SourceCount int                    `json:"sourceCount"`
	Results     []ProviderImportResult `json:"results"`
}

// NewService constructs the windows registration application service.
func NewService(worker Worker, accounts AccountImporter) *Service {
	return &Service{worker: worker, accounts: accounts}
}

// Status proxies the worker snapshot.
func (s *Service) Status() windowsregisterinfra.Status {
	if s == nil || s.worker == nil {
		return windowsregisterinfra.Status{
			PlatformSupported: false,
			Ready:             false,
			Missing:           []string{"service"},
			State:             windowsregisterinfra.StateIdle,
			Logs:              []string{},
		}
	}
	return s.worker.Status()
}

// Start validates and starts the worker.
func (s *Service) Start(opts windowsregisterinfra.StartOptions) (windowsregisterinfra.Status, error) {
	if s == nil || s.worker == nil {
		return windowsregisterinfra.Status{}, windowsregisterinfra.ErrPlatformUnsupported
	}
	if strings.TrimSpace(opts.EmailMode) == "" {
		opts.EmailMode = "tempmail"
	}
	return s.worker.Start(opts)
}

// Stop stops the worker.
func (s *Service) Stop(ctx context.Context) (windowsregisterinfra.Status, error) {
	if s == nil || s.worker == nil {
		return windowsregisterinfra.Status{}, windowsregisterinfra.ErrPlatformUnsupported
	}
	return s.worker.Stop(ctx)
}

// Import loads registration SSO tokens into selected account pools.
func (s *Service) Import(ctx context.Context, req ImportRequest) (ImportResponse, error) {
	if s == nil || s.worker == nil || s.accounts == nil {
		return ImportResponse{}, windowsregisterinfra.ErrPlatformUnsupported
	}
	scope := strings.TrimSpace(strings.ToLower(req.Scope))
	if scope == "" {
		scope = "current"
	}
	destinations := normalizeDestinations(req.Destinations)
	if len(destinations) == 0 {
		return ImportResponse{}, fmt.Errorf("%w: destinations required", windowsregisterinfra.ErrInvalidStartOptions)
	}

	tokens, err := s.worker.ImportTokens(scope)
	if err != nil {
		return ImportResponse{}, err
	}
	payload := []byte(strings.Join(tokens, "\n"))
	response := ImportResponse{
		Scope:       scope,
		SourceCount: len(tokens),
		Results:     make([]ProviderImportResult, 0, len(destinations)),
	}
	for _, destination := range destinations {
		item := ProviderImportResult{Provider: destination}
		var result accountapp.ImportResult
		var importErr error
		switch destination {
		case "grok_web":
			result, importErr = s.accounts.ImportWebCredentials(ctx, payload)
		case "grok_console":
			result, importErr = s.accounts.ImportConsoleCredentials(ctx, payload)
		default:
			importErr = fmt.Errorf("unsupported destination %q", destination)
		}
		if importErr != nil {
			item.Error = importErr.Error()
		} else {
			item.Created = result.Created
			item.Updated = result.Updated
			item.Skipped = result.Skipped
		}
		response.Results = append(response.Results, item)
	}
	return response, nil
}

func normalizeDestinations(values []string) []string {
	if len(values) == 0 {
		return []string{"grok_web", "grok_console"}
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		switch value {
		case "grok_web", "grok_console":
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}
