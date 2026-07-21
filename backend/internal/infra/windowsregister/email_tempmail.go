package windowsregister

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var defaultMailTMBases = []string{
	"https://api.mail.tm",
	"https://api.mail.gw",
	"https://api.duckmail.sbs",
}

type TempMailProvider struct {
	client        *http.Client
	lolBase       string
	mailTMBase    []string
	pollInterval  time.Duration
	pollTimeout   time.Duration
	addressSource func(string) (string, error)
}

type tempMailHandle struct {
	Kind  string `json:"kind"`
	Base  string `json:"base"`
	Token string `json:"token"`
}

type mailTMDomain struct {
	Domain    string `json:"domain"`
	IsActive  *bool  `json:"isActive"`
	IsPrivate bool   `json:"isPrivate"`
}

func NewTempMailProvider(client *http.Client) *TempMailProvider {
	return &TempMailProvider{
		client:        configuredHTTPClient(client),
		lolBase:       "https://api.tempmail.lol",
		mailTMBase:    append([]string(nil), defaultMailTMBases...),
		pollInterval:  defaultPollInterval,
		pollTimeout:   defaultPollTimeout,
		addressSource: randomMailboxAddress,
	}
}

func (p *TempMailProvider) Create(ctx context.Context) (Mailbox, error) {
	password, err := randomMailboxPassword()
	if err != nil {
		return Mailbox{}, err
	}
	if mailbox, err := p.createLOL(ctx, password); err == nil {
		return mailbox, nil
	}
	for _, base := range p.mailTMBase {
		if mailbox, err := p.createMailTM(ctx, base, password); err == nil {
			return mailbox, nil
		}
		if ctx.Err() != nil {
			return Mailbox{}, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return Mailbox{}, ctx.Err()
	}
	return Mailbox{}, ErrEmailProviderUnavailable
}

func (p *TempMailProvider) PollCode(ctx context.Context, mailbox Mailbox) (string, error) {
	var handle tempMailHandle
	if len(mailbox.Handle) == 0 || json.Unmarshal(mailbox.Handle, &handle) != nil || handle.Token == "" {
		return "", ErrEmailResponse
	}
	return pollEmailCode(ctx, p.pollInterval, p.pollTimeout, func(pollContext context.Context) (string, error) {
		switch handle.Kind {
		case "lol":
			return p.fetchLOL(pollContext, handle)
		case "mailtm":
			return p.fetchMailTM(pollContext, handle)
		default:
			return "", ErrEmailResponse
		}
	})
}

func (p *TempMailProvider) createLOL(ctx context.Context, password string) (Mailbox, error) {
	baseURL, err := normalizeEmailBase(p.lolBase)
	if err != nil {
		return Mailbox{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v2/inbox/create", nil)
	if err != nil {
		return Mailbox{}, ErrEmailResponse
	}
	var response struct {
		Address string `json:"address"`
		Token   string `json:"token"`
	}
	if err := readEmailJSON(ctx, p.client, request, &response); err != nil {
		return Mailbox{}, err
	}
	if !mailboxAddressValid(response.Address) || strings.TrimSpace(response.Token) == "" {
		return Mailbox{}, ErrEmailResponse
	}
	handle, _ := json.Marshal(tempMailHandle{Kind: "lol", Base: baseURL, Token: response.Token})
	return Mailbox{Address: response.Address, Password: password, Handle: handle}, nil
}

func (p *TempMailProvider) createMailTM(ctx context.Context, rawBase, password string) (Mailbox, error) {
	baseURL, err := normalizeEmailBase(rawBase)
	if err != nil {
		return Mailbox{}, err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/domains", nil)
	var raw json.RawMessage
	if err := readEmailJSON(ctx, p.client, request, &raw); err != nil {
		return Mailbox{}, err
	}
	domains := decodeMailTMDomains(raw)
	domain := ""
	for _, candidate := range domains {
		active := candidate.IsActive == nil || *candidate.IsActive
		if active && !candidate.IsPrivate && strings.TrimSpace(candidate.Domain) != "" {
			domain = candidate.Domain
			break
		}
	}
	if domain == "" {
		return Mailbox{}, ErrEmailResponse
	}
	address, err := p.addressSource(domain)
	if err != nil {
		return Mailbox{}, err
	}
	payload, _ := json.Marshal(map[string]string{"address": address, "password": password})
	request, _ = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/accounts", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if err := checkEmailStatus(ctx, p.client, request); err != nil {
		return Mailbox{}, err
	}
	request, _ = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/token", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	var tokenResponse struct {
		Token string `json:"token"`
	}
	if err := readEmailJSON(ctx, p.client, request, &tokenResponse); err != nil || strings.TrimSpace(tokenResponse.Token) == "" {
		return Mailbox{}, ErrEmailResponse
	}
	handle, _ := json.Marshal(tempMailHandle{Kind: "mailtm", Base: baseURL, Token: tokenResponse.Token})
	return Mailbox{Address: address, Password: password, Handle: handle}, nil
}

func (p *TempMailProvider) fetchLOL(ctx context.Context, handle tempMailHandle) (string, error) {
	baseURL, err := normalizeEmailBase(handle.Base)
	if err != nil {
		return "", ErrEmailResponse
	}
	endpoint, _ := url.Parse(baseURL + "/v2/inbox")
	query := endpoint.Query()
	query.Set("token", handle.Token)
	endpoint.RawQuery = query.Encode()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	var payload struct {
		Emails   []map[string]json.RawMessage `json:"emails"`
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := readEmailJSON(ctx, p.client, request, &payload); err != nil {
		return "", err
	}
	messages := payload.Emails
	if len(messages) == 0 {
		messages = payload.Messages
	}
	return extractCodeFromMessages(messages), nil
}

func (p *TempMailProvider) fetchMailTM(ctx context.Context, handle tempMailHandle) (string, error) {
	baseURL, err := normalizeEmailBase(handle.Base)
	if err != nil {
		return "", ErrEmailResponse
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/messages", nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+handle.Token)
	var raw json.RawMessage
	if err := readEmailJSON(ctx, p.client, request, &raw); err != nil {
		return "", err
	}
	messages := decodeMailTMMessages(raw)
	if len(messages) == 0 {
		return "", nil
	}
	messageID := jsonText(messages[0]["id"])
	if messageID == "" || len(messageID) > 256 {
		return "", ErrEmailResponse
	}
	request, _ = http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/messages/"+url.PathEscape(messageID), nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+handle.Token)
	var detail map[string]json.RawMessage
	if err := readEmailJSON(ctx, p.client, request, &detail); err != nil {
		return "", err
	}
	return extractCodeFromMessages([]map[string]json.RawMessage{detail}), nil
}

func decodeMailTMDomains(raw json.RawMessage) []mailTMDomain {
	var envelope struct {
		Members []mailTMDomain `json:"hydra:member"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Members) > 0 {
		return envelope.Members
	}
	var domains []mailTMDomain
	_ = json.Unmarshal(raw, &domains)
	return domains
}

func decodeMailTMMessages(raw json.RawMessage) []map[string]json.RawMessage {
	var envelope struct {
		Members []map[string]json.RawMessage `json:"hydra:member"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Members) > 0 {
		return envelope.Members
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &messages)
	return messages
}

func extractCodeFromMessages(messages []map[string]json.RawMessage) string {
	var content strings.Builder
	for _, message := range messages {
		for _, field := range []string{"subject", "body", "intro", "text", "html"} {
			appendJSONText(&content, message[field])
			content.WriteByte('\n')
		}
	}
	return extractEmailCode(content.String())
}

func appendJSONText(builder *strings.Builder, raw json.RawMessage) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		builder.WriteString(value)
		return
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		builder.WriteString(strings.Join(values, "\n"))
	}
}

func jsonText(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func mailboxAddressValid(address string) bool {
	separator := strings.LastIndexByte(strings.TrimSpace(address), '@')
	return separator > 0 && separator < len(strings.TrimSpace(address))-1 && !strings.ContainsAny(address, "\r\n")
}
