package inference

import (
	"encoding/json"
	"net/http"
	"strings"
)

const maxPromptCacheSeedBytes = 1024

// extractPromptCacheSeed 提取客户端会话标识；真正发往上游的 key 会在 Gateway 中隔离并哈希。
// 兼容 Claude Code / Codex / Trae / zcode / Sub2API（session_id、conversation_id、prompt_cache_key）。
func extractPromptCacheSeed(headers http.Header, body []byte) string {
	if headers != nil {
		// 优先级：专用 Agent 会话头 → 通用 session/conversation → 反代透传。
		// 注意：不要使用 X-Request-Id / X-Client-Request-Id 等“每请求唯一”字段。
		for _, name := range []string{
			"X-Claude-Code-Session-Id",
			"X-Codex-Session-Id",
			"X-Trae-Session-Id",
			"X-Zcode-Session-Id",
			"X-Chat-Id", "X-Chat-ID",
			"X-Thread-Id", "X-Thread-ID",
			"X-Conversation-Id", "Conversation-Id", "Conversation_id", "conversation_id",
			"OpenAI-Conversation-Id", "X-OpenAI-Conversation-Id",
			"X-Session-ID", "X-Session-Id", "Session-Id", "Session_id", "session_id",
			// Sub2API / 反代偶发透传
			"X-Client-Session-Id", "X-Grok-Conv-Id", "x-grok-conv-id", "X-Grok-Session-Id",
		} {
			if seed := normalizePromptCacheSeed(headers.Get(name)); seed != "" {
				return seed
			}
		}
	}
	var payload struct {
		PromptCacheKey      string `json:"prompt_cache_key"`
		ConversationID      string `json:"conversation_id"`
		ConversationIDCamel string `json:"conversationId"`
		SessionID           string `json:"session_id"`
		SessionIDCamel      string `json:"sessionId"`
		ChatID              string `json:"chat_id"`
		ChatIDCamel         string `json:"chatId"`
		ThreadID            string `json:"thread_id"`
		ThreadIDCamel       string `json:"threadId"`
		Metadata            struct {
			SessionID           string `json:"session_id"`
			SessionIDCamel      string `json:"sessionId"`
			ConversationID      string `json:"conversation_id"`
			ConversationIDCamel string `json:"conversationId"`
			ChatID              string `json:"chat_id"`
			ChatIDCamel         string `json:"chatId"`
			ThreadID            string `json:"thread_id"`
			ThreadIDCamel       string `json:"threadId"`
			UserID              string `json:"user_id"`
			UserIDCamel         string `json:"userId"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	// body.prompt_cache_key 在 handler 里也会进 PromptCacheKey；这里再提取一次，
	// 保证仅依赖 seed 路径的中间件/日志也能看到。
	for _, candidate := range []string{
		payload.PromptCacheKey,
		payload.Metadata.SessionID,
		payload.Metadata.SessionIDCamel,
		payload.Metadata.ConversationID,
		payload.Metadata.ConversationIDCamel,
		payload.Metadata.ChatID,
		payload.Metadata.ChatIDCamel,
		payload.Metadata.ThreadID,
		payload.Metadata.ThreadIDCamel,
		promptCacheSeedFromUserID(payload.Metadata.UserID),
		promptCacheSeedFromUserID(payload.Metadata.UserIDCamel),
		payload.SessionID,
		payload.SessionIDCamel,
		payload.ConversationID,
		payload.ConversationIDCamel,
		payload.ChatID,
		payload.ChatIDCamel,
		payload.ThreadID,
		payload.ThreadIDCamel,
	} {
		if seed := normalizePromptCacheSeed(candidate); seed != "" {
			return seed
		}
	}
	return ""
}

func promptCacheSeedFromUserID(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	var embedded struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
	}
	if json.Unmarshal([]byte(userID), &embedded) == nil {
		if seed := normalizePromptCacheSeed(embedded.SessionID); seed != "" {
			return seed
		}
		if seed := normalizePromptCacheSeed(embedded.SessionIDCamel); seed != "" {
			return seed
		}
	}
	const marker = "_session_"
	if index := strings.LastIndex(userID, marker); index >= 0 {
		return normalizePromptCacheSeed(userID[index+len(marker):])
	}
	return ""
}

func normalizePromptCacheSeed(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxPromptCacheSeedBytes {
		return ""
	}
	return value
}
