package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
)

// v3: soft 会话改为优先使用 system/tools 稳定前缀，避免长会话截断首条 user 后身份漂移。
const buildSessionIdentityVersion = "v3"

type buildSessionIdentity struct {
	// upstreamID is sent as prompt_cache_key and x-grok-conv-id and must remain stable across turns.
	upstreamID string
	// affinityKey controls account stickiness and is isolated by model to avoid cross-model collisions.
	affinityKey string
	// replayKey is derived only from explicit client session signals; soft anchors must not drive encrypted reasoning replay.
	replayKey string
	// soft indicates a fallback identity derived from message content when no explicit session is available.
	soft bool
}

// resolveBuildSessionIdentity 对齐 CPA 官方缓存会话策略，并针对 Claude Code / Codex / Trae 加固：
// 1) 显式 prompt_cache_key / session seed → 稳定哈希身份（多租户隔离）
// 2) 否则用稳定前缀（system/instructions + tools）生成 soft 身份，保证多轮粘滞与 conv-id 不漂移
// 3) 没有稳定前缀时，再退回首条 user 锚点
// 4) 完全无信号 → 空身份（禁止每请求随机 ID，避免打散 xAI 服务器亲和）
// resolveBuildSessionIdentity derives a stable Grok Build session identity:
// 1. Prefer explicit client session signals, isolated by client key, provider, and model.
// 2. Fall back to system/instructions and the first user message when no explicit signal exists.
// 3. Return an empty identity when no signal exists; never generate a random session ID per request.
func resolveBuildSessionIdentity(clientKeyID uint64, provider accountdomain.Provider, upstreamModel, explicitKey, sessionSeed string, body []byte) buildSessionIdentity {
	// Prefer Claude Code and Codex session signals extracted by the transport layer.
	// body.prompt_cache_key is only a fallback when no stronger header or session signal exists.
	seed := strings.TrimSpace(sessionSeed)
	if seed == "" {
		seed = strings.TrimSpace(explicitKey)
	}
	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	if clientKeyID == 0 || provider == "" || model == "" {
		return buildSessionIdentity{}
	}
	if seed != "" {
		upstreamSource := fmt.Sprintf("grok2api:build-session:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, seed)
		affinitySource := fmt.Sprintf("grok2api:build-affinity:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, seed)
		replaySource := fmt.Sprintf("grok2api:build-replay:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, seed)
		return buildSessionIdentity{
			upstreamID:  digestUUID(upstreamSource),
			affinityKey: hexDigest(affinitySource),
			replayKey:   hexDigest(replaySource),
		}
	}

	// 无显式 session 时，优先用 system + tools 这类跨轮稳定前缀。
	// Claude Code / Codex 长会话会截断早期 user 消息，把 firstUser 编入身份会导致 conv-id 漂移、缓存率接近 0。
	// Fall back to a message-prefix hash to keep account affinity and session IDs stable without client session signals.
	system, firstUser, _ := extractMessageAnchors(body)
	toolsFingerprint := extractToolsFingerprint(body)
	system = truncateAnchor(system, 400)
	firstUser = truncateAnchor(firstUser, 200)

	if system != "" || toolsFingerprint != "" {
		// Keep both upstream session and account affinity model-scoped so multi-model
		// clients do not share x-grok-conv-id / prompt_cache_key.
		upstreamSource := fmt.Sprintf("grok2api:build-soft-prefix:%s:%d:%s:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, system, toolsFingerprint)
		affinitySource := fmt.Sprintf("grok2api:build-soft-prefix-affinity:%s:%d:%s:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, system, toolsFingerprint)
		return buildSessionIdentity{
			upstreamID:  digestUUID(upstreamSource),
			affinityKey: hexDigest(affinitySource),
			soft:        true,
		}
	}
	if firstUser == "" {
		return buildSessionIdentity{}
	}
	// system/tools already handled above; fall back to first-user soft identity only.
	upstreamSource := fmt.Sprintf("grok2api:build-soft-session:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, firstUser)
	affinitySource := fmt.Sprintf("grok2api:build-soft-affinity:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, firstUser)
	return buildSessionIdentity{
		upstreamID:  digestUUID(upstreamSource),
		affinityKey: hexDigest(affinitySource),
		soft:        true,
	}
}

// rebuildBuildAffinityKey 在 previous_response_id 继承上游会话时，补回账号粘滞键。
func rebuildBuildAffinityKey(clientKeyID uint64, provider accountdomain.Provider, upstreamModel, upstreamSessionID string) string {
	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	sessionID := strings.TrimSpace(upstreamSessionID)
	if clientKeyID == 0 || provider == "" || model == "" || sessionID == "" {
		return ""
	}
	return hexDigest(fmt.Sprintf("grok2api:build-affinity-restore:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, sessionID))
}

func digestUUID(source string) string {
	digest := sha256.Sum256([]byte(source))
	hexID := hex.EncodeToString(digest[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexID[0:8], hexID[8:12], hexID[12:16], hexID[16:20], hexID[20:32])
}

func hexDigest(source string) string {
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

func truncateAnchor(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return value
	}
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

// extractMessageAnchors extracts stable prefix anchors from Chat, Messages, and Responses request bodies.
// It uses only system, the first user message, and an optional first assistant message to avoid hash drift across turns.
func extractMessageAnchors(body []byte) (system, firstUser, firstAssistant string) {
	if len(body) == 0 {
		return "", "", ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return "", "", ""
	}
	// Top-level system or instructions fields provide a stable system anchor for OpenAI Responses and Chat.
	if raw, ok := root["instructions"]; ok {
		system = flattenMessageContent(raw)
	}
	if system == "" {
		if raw, ok := root["system"]; ok {
			system = flattenMessageContent(raw)
		}
	}
	if raw, ok := root["messages"]; ok {
		msgSystem, msgUser, msgAssistant := anchorsFromRoleMessages(raw)
		if system == "" {
			system = msgSystem
		}
		firstUser, firstAssistant = msgUser, msgAssistant
		if firstUser != "" {
			return system, firstUser, firstAssistant
		}
	}
	if raw, ok := root["input"]; ok {
		inSystem, inUser, inAssistant := anchorsFromResponsesInput(raw)
		if system == "" {
			system = inSystem
		}
		if firstUser == "" {
			firstUser = inUser
		}
		if firstAssistant == "" {
			firstAssistant = inAssistant
		}
	}
	return system, firstUser, firstAssistant
}

// extractToolsFingerprint 提取 tools 定义的稳定指纹。Agent 客户端跨轮 tools schema 通常稳定，
// 适合作为 soft prompt-cache 会话身份的一部分。
func extractToolsFingerprint(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	raw, ok := root["tools"]
	if !ok || len(bytesTrimSpace(raw)) == 0 || string(bytesTrimSpace(raw)) == "null" {
		return ""
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 {
		// 非数组 tools（少见）仍做整体哈希，避免完全丢失信号。
		return hexDigest("tools-raw:" + string(raw))
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		var parsed map[string]json.RawMessage
		if json.Unmarshal(tool, &parsed) != nil {
			continue
		}
		name := toolNameFromRaw(parsed)
		if name == "" {
			// Anthropic 自定义工具可能只有 name；OpenAI function 在 function.name。
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return hexDigest("tools-count:" + fmt.Sprintf("%d", len(tools)))
	}
	sort.Strings(names)
	return hexDigest("tools:" + strings.Join(names, "\x00"))
}

func toolNameFromRaw(parsed map[string]json.RawMessage) string {
	for _, key := range []string{"name", "type"} {
		var value string
		if raw, ok := parsed[key]; ok && json.Unmarshal(raw, &value) == nil {
			value = strings.TrimSpace(value)
			if value != "" && value != "function" && value != "custom" {
				return value
			}
		}
	}
	if raw, ok := parsed["function"]; ok {
		var function map[string]json.RawMessage
		if json.Unmarshal(raw, &function) == nil {
			var name string
			if fn, ok := function["name"]; ok && json.Unmarshal(fn, &name) == nil {
				return strings.TrimSpace(name)
			}
		}
	}
	return ""
}

func bytesTrimSpace(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func anchorsFromRoleMessages(raw json.RawMessage) (system, firstUser, firstAssistant string) {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return "", "", ""
	}
	for _, msg := range messages {
		var role string
		_ = json.Unmarshal(msg["role"], &role)
		content := flattenMessageContent(msg["content"])
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "system":
			if system == "" {
				system = content
			}
		case "user":
			if firstUser == "" {
				firstUser = content
			}
		case "assistant":
			if firstAssistant == "" {
				firstAssistant = content
			}
		}
		if system != "" && firstUser != "" && firstAssistant != "" {
			break
		}
	}
	return system, firstUser, firstAssistant
}

func anchorsFromResponsesInput(raw json.RawMessage) (system, firstUser, firstAssistant string) {
	// Shorthand form: input is a direct string.
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return "", strings.TrimSpace(asString), ""
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return "", "", ""
	}
	for _, item := range items {
		var typeName, role string
		_ = json.Unmarshal(item["type"], &typeName)
		_ = json.Unmarshal(item["role"], &role)
		typeName = strings.TrimSpace(typeName)
		role = strings.ToLower(strings.TrimSpace(role))
		// Top-level instructions handle the system anchor; this branch extracts messages.
		if typeName != "" && typeName != "message" {
			continue
		}
		content := flattenMessageContent(item["content"])
		if content == "" {
			// Support content objects whose text field is a string.
			var text string
			if json.Unmarshal(item["text"], &text) == nil {
				content = strings.TrimSpace(text)
			}
		}
		if content == "" {
			continue
		}
		switch role {
		case "system", "developer":
			if system == "" {
				system = content
			}
		case "user":
			if firstUser == "" {
				firstUser = content
			}
		case "assistant":
			if firstAssistant == "" {
				firstAssistant = content
			}
		default:
			// Treat role-less plain-text input items as user input.
			if role == "" && firstUser == "" && (typeName == "" || typeName == "message") {
				firstUser = content
			}
		}
		if firstUser != "" && firstAssistant != "" {
			break
		}
	}
	// Use top-level instructions as a system fallback.
	return system, firstUser, firstAssistant
}

func flattenMessageContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return strings.TrimSpace(asString)
	}
	// Anthropic system 可为 content block 数组。
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		var partType string
		_ = json.Unmarshal(part["type"], &partType)
		switch strings.TrimSpace(partType) {
		case "", "text", "input_text", "output_text":
			var text string
			if json.Unmarshal(part["text"], &text) == nil && strings.TrimSpace(text) != "" {
				if builder.Len() > 0 {
					builder.WriteByte('\n')
				}
				builder.WriteString(strings.TrimSpace(text))
			}
		}
	}
	return builder.String()
}
