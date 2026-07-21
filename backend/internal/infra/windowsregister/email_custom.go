package windowsregister

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type CustomEmailProvider struct {
	baseURL       string
	domain        string
	client        *http.Client
	pollInterval  time.Duration
	pollTimeout   time.Duration
	addressSource func(string) (string, error)
}

func NewCustomEmailProvider(baseURL, domain string, client *http.Client) *CustomEmailProvider {
	return &CustomEmailProvider{
		baseURL:       strings.TrimSpace(baseURL),
		domain:        strings.TrimSpace(domain),
		client:        configuredHTTPClient(client),
		pollInterval:  defaultPollInterval,
		pollTimeout:   defaultPollTimeout,
		addressSource: randomMailboxAddress,
	}
}

func (p *CustomEmailProvider) Create(context.Context) (Mailbox, error) {
	if _, err := normalizeEmailBase(p.baseURL); err != nil {
		return Mailbox{}, err
	}
	address, err := p.addressSource(p.domain)
	if err != nil || !mailboxDomainMatches(address, p.domain) {
		return Mailbox{}, ErrEmailDomain
	}
	password, err := randomMailboxPassword()
	if err != nil {
		return Mailbox{}, err
	}
	return Mailbox{Address: address, Password: password}, nil
}

func (p *CustomEmailProvider) PollCode(ctx context.Context, mailbox Mailbox) (string, error) {
	if !mailboxDomainMatches(mailbox.Address, p.domain) {
		return "", ErrEmailDomain
	}
	baseURL, err := normalizeEmailBase(p.baseURL)
	if err != nil {
		return "", err
	}
	return pollEmailCode(ctx, p.pollInterval, p.pollTimeout, func(pollContext context.Context) (string, error) {
		request, err := http.NewRequestWithContext(pollContext, http.MethodGet, baseURL+"/check/"+url.PathEscape(mailbox.Address), nil)
		if err != nil {
			return "", ErrEmailResponse
		}
		var response struct {
			Code string `json:"code"`
		}
		if err := readEmailJSON(pollContext, p.client, request, &response); err != nil {
			return "", err
		}
		return strings.TrimSpace(response.Code), nil
	})
}
