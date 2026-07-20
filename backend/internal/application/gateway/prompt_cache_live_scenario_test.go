package gateway

import (
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
)

// 模拟 Claude Code / Codex / Trae 多轮 Agent 请求，验证修复后的缓存会话身份。
func TestLiveScenario_ClaudeCodeStyleMultiTurnCacheAffinity(t *testing.T) {
	const clientKeyID uint64 = 42
	model := "grok-4.5"

	// 第 1 轮：完整历史
	turn1 := []byte(`{
		"model":"grok-4.5",
		"system":"You are Claude Code, a coding agent in the terminal.",
		"tools":[
			{"name":"Read"},
			{"name":"Bash"},
			{"name":"Edit"},
			{"type":"function","function":{"name":"Glob"}}
		],
		"messages":[
			{"role":"user","content":"请阅读 selector.go 并解释粘滞逻辑"}
		]
	}`)

	// 第 2 轮：追加对话
	turn2 := []byte(`{
		"model":"grok-4.5",
		"system":"You are Claude Code, a coding agent in the terminal.",
		"tools":[
			{"name":"Read"},
			{"name":"Bash"},
			{"name":"Edit"},
			{"type":"function","function":{"name":"Glob"}}
		],
		"messages":[
			{"role":"user","content":"请阅读 selector.go 并解释粘滞逻辑"},
			{"role":"assistant","content":"粘滞会优先复用绑定账号。"},
			{"role":"user","content":"那缓存亲和呢？"}
		]
	}`)

	// 第 N 轮：长会话截断，最早 user 已不在上下文
	turnN := []byte(`{
		"model":"grok-4.5",
		"system":"You are Claude Code, a coding agent in the terminal.",
		"tools":[
			{"type":"function","function":{"name":"Glob"}},
			{"name":"Edit"},
			{"name":"Bash"},
			{"name":"Read"}
		],
		"messages":[
			{"role":"user","content":"继续优化缓存命中"},
			{"role":"assistant","content":"可以先固定 session。"},
			{"role":"user","content":"再验证截断场景"}
		]
	}`)

	// 显式 Claude session 头路径
	seed := "claude-code-session-live-001"
	explicit1 := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", seed, turn1)
	explicit2 := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", seed, turn2)
	explicitN := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", seed, turnN)
	if explicit1.upstreamID == "" || explicit1.upstreamID != explicit2.upstreamID || explicit1.upstreamID != explicitN.upstreamID {
		t.Fatalf("explicit claude session drifted: t1=%#v t2=%#v tN=%#v", explicit1, explicit2, explicitN)
	}
	if explicit1.affinityKey == "" || explicit1.affinityKey != explicit2.affinityKey || explicit1.affinityKey != explicitN.affinityKey {
		t.Fatalf("explicit affinity drifted: t1=%#v t2=%#v tN=%#v", explicit1, explicit2, explicitN)
	}

	// 无 session 时，靠 system+tools soft 前缀，截断后仍稳定
	soft1 := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", "", turn1)
	soft2 := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", "", turn2)
	softN := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", "", turnN)
	if !soft1.soft || soft1.upstreamID == "" {
		t.Fatalf("expected soft prefix identity, got %#v", soft1)
	}
	if soft1.upstreamID != soft2.upstreamID || soft1.upstreamID != softN.upstreamID {
		t.Fatalf("soft session drifted across turns/truncation:\n t1=%s\n t2=%s\n tN=%s", soft1.upstreamID, soft2.upstreamID, softN.upstreamID)
	}
	if soft1.affinityKey != soft2.affinityKey || soft1.affinityKey != softN.affinityKey {
		t.Fatalf("soft affinity drifted:\n t1=%s\n t2=%s\n tN=%s", soft1.affinityKey, soft2.affinityKey, softN.affinityKey)
	}

	// 不同 client key 必须隔离
	otherTenant := resolveBuildSessionIdentity(99, accountdomain.ProviderBuild, model, "", seed, turn1)
	if otherTenant.upstreamID == explicit1.upstreamID {
		t.Fatal("tenant isolation broken")
	}

	// 不同 tools 配置必须隔离，避免跨 Agent 污染
	otherTools := []byte(`{
		"system":"You are Claude Code, a coding agent in the terminal.",
		"tools":[{"name":"Read"},{"name":"Write"}],
		"messages":[{"role":"user","content":"继续优化缓存命中"}]
	}`)
	otherSoft := resolveBuildSessionIdentity(clientKeyID, accountdomain.ProviderBuild, model, "", "", otherTools)
	if otherSoft.upstreamID == soft1.upstreamID {
		t.Fatal("different tools must isolate soft cache identity")
	}

	t.Logf("PASS explicit session=%s affinity=%s", explicit1.upstreamID, explicit1.affinityKey[:16]+"...")
	t.Logf("PASS soft session=%s affinity=%s", soft1.upstreamID, soft1.affinityKey[:16]+"...")
}
