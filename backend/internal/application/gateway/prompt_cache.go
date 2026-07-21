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
	// upstreamID 写入上游 prompt_cache_key 与 x-grok-conv-id，须跨轮稳定。
	upstreamID string
	// affinityKey 用于账号粘滞；按模型隔离，避免不同模型能力账号互相覆盖。
	affinityKey string
	// replayKey 仅由客户端显式会话信号生成；soft 消息锚点不得驱动 encrypted reasoning 回放。
	replayKey string
	// soft 表示由消息内容兜底生成（无显式 session 时），仍须稳定。
	soft bool
}

// resolveBuildSessionIdentity 对齐 CPA 官方缓存会话策略，并针对 Claude Code / Codex / Trae 加固：
// 1) 显式 prompt_cache_key / session seed → 稳定哈希身份（多租户隔离）
// 2) 否则用稳定前缀（system/instructions + tools）生成 soft 身份，保证多轮粘滞与 conv-id 不漂移
// 3) 没有稳定前缀时，再退回首条 user 锚点
// 4) 完全无信号 → 空身份（禁止每请求随机 ID，避免打散 xAI 服务器亲和）
func resolveBuildSessionIdentity(clientKeyID uint64, provider accountdomain.Provider, upstreamModel, explicitKey, sessionSeed string, body []byte) buildSessionIdentity {
	seed := strings.TrimSpace(explicitKey)
	if seed == "" {
		seed = strings.TrimSpace(sessionSeed)
	}
	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	if clientKeyID == 0 || provider == "" || model == "" {
		return buildSessionIdentity{}
	}
	if seed != "" {
		upstreamSource := fmt.Sprintf("grok2api:build-session:%s:%d:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, seed)
		affinitySource := fmt.Sprintf("grok2api:build-affinity:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, model, seed)
		replaySource := fmt.Sprintf("grok2api:build-replay:%s:%d:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, seed)
		return buildSessionIdentity{
			upstreamID:  digestUUID(upstreamSource),
			affinityKey: hexDigest(affinitySource),
			replayKey:   hexDigest(replaySource),
		}
	}

	// 无显式 session 时，优先用 system + tools 这类跨轮稳定前缀。
	// Claude Code / Codex 长会话会截断早期 user 消息，把 firstUser 编入身份会导致 conv-id 漂移、缓存率接近 0。
	system, firstUser, _ := extractMessageAnchors(body)
	toolsFingerprint := extractToolsFingerprint(body)
	system = truncateAnchor(system, 400)
	firstUser = truncateAnchor(firstUser, 200)

	if system != "" || toolsFingerprint != "" {
		upstreamSource := fmt.Sprintf("grok2api:build-soft-prefix:%s:%d:%s:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, system, toolsFingerprint)
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
	upstreamSource := fmt.Sprintf("grok2api:build-soft-session:%s:%d:%s:%s", buildSessionIdentityVersion, clientKeyID, provider, firstUser)
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

// extractMessageAnchors 从 Chat / Messages / Responses 请求体提取稳定前缀锚点。
// 仅使用 system + 首条 user（及可选首条 assistant），避免后续轮次追加导致 hash 漂移。
func extractMessageAnchors(body []byte) (system, firstUser, firstAssistant string) {
	if len(body) == 0 {
		return "", "", ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return "", "", ""
	}
	// 顶层 system / instructions（OpenAI Responses / Chat 常见）作为稳定前缀 system 锚点。
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
	// 简写：input 直接是字符串
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
		// instructions 级 system 由顶层处理；此处抓 message
		if typeName != "" && typeName != "message" {
			continue
		}
		content := flattenMessageContent(item["content"])
		if content == "" {
			// 兼容 content 为字符串字段 text
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
			// 无 role 的纯文本 input 项视为 user
			if role == "" && firstUser == "" && (typeName == "" || typeName == "message") {
				firstUser = content
			}
		}
		if firstUser != "" && firstAssistant != "" {
			break
		}
	}
	// 顶层 instructions 作为 system 补充
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
