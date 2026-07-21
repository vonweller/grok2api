package gateway

import (
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
)

func TestResolveBuildSessionIdentityIsStableAndTenantIsolated(t *testing.T) {
	base := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "session-1", nil)
	if len(base.upstreamID) != 36 || len(base.affinityKey) != 64 || len(base.replayKey) != 64 || base != resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "session-1", nil) {
		t.Fatalf("unstable identity = %#v", base)
	}
	for name, value := range map[string]buildSessionIdentity{
		"client":   resolveBuildSessionIdentity(8, accountdomain.ProviderBuild, "grok-4.5", "", "session-1", nil),
		"provider": resolveBuildSessionIdentity(7, accountdomain.ProviderConsole, "grok-4.5", "", "session-1", nil),
		"session":  resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "session-2", nil),
	} {
		if value.upstreamID == base.upstreamID || value.affinityKey == base.affinityKey {
			t.Fatalf("%s was not isolated: %#v vs %#v", name, value, base)
		}
	}
}

func TestResolveBuildSessionIdentitySeparatesAffinityFromUpstreamSession(t *testing.T) {
	first := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "session-1", nil)
	otherModel := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.3", "", "session-1", nil)
	if first.upstreamID == "" || first.upstreamID == otherModel.upstreamID || first.replayKey == otherModel.replayKey {
		t.Fatalf("model-specific upstream session was not isolated: first=%#v other=%#v", first, otherModel)
	}
	if first.affinityKey == otherModel.affinityKey {
		t.Fatalf("model-specific account affinity was not isolated: %#v", first)
	}
}

func TestResolveBuildSoftSessionIdentityIsModelScoped(t *testing.T) {
	body := []byte(`{"instructions":"stable system","input":[{"role":"user","content":"hello"}]}`)
	first := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", body)
	otherModel := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.3", "", "", body)
	if first.upstreamID == "" || first.affinityKey == "" || !first.soft || !otherModel.soft {
		t.Fatalf("soft identity missing: first=%#v other=%#v", first, otherModel)
	}
	if first.upstreamID == otherModel.upstreamID || first.affinityKey == otherModel.affinityKey {
		t.Fatalf("soft identity must be model scoped: first=%#v other=%#v", first, otherModel)
	}
}

func TestResolveBuildSessionIdentityPrefersSessionSignal(t *testing.T) {
	first := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "client-key", "session-1", nil)
	second := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "client-key", "session-2", nil)
	if first.upstreamID == "" || first == second {
		t.Fatalf("session signal did not take precedence: first=%#v second=%#v", first, second)
	}
	clientKeyOnly := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "client-key", "", nil)
	if clientKeyOnly.upstreamID == "" || clientKeyOnly == first {
		t.Fatalf("explicit key fallback was not isolated: session=%#v explicit=%#v", first, clientKeyOnly)
	}
	if value := resolveBuildSessionIdentity(0, accountdomain.ProviderBuild, "grok-4.5", "client-key", "", nil); value != (buildSessionIdentity{}) {
		t.Fatal("identity without client ownership should be empty")
	}
	if value := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", nil); value != (buildSessionIdentity{}) {
		t.Fatal("identity without an explicit key or session or body should be empty")
	}
}

func TestResolveBuildSessionIdentitySoftFromMessagesIsStableAcrossTurns(t *testing.T) {
	turn1 := []byte(`{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hello world"}]}`)
	turn2 := []byte(`{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hello world"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`)
	first := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", turn1)
	second := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", turn2)
	if !first.soft || first.upstreamID == "" {
		t.Fatalf("expected soft identity, got %#v", first)
	}
	if first.replayKey != "" {
		t.Fatalf("soft identity must not enable reasoning replay: %#v", first)
	}
	if first.upstreamID != second.upstreamID || first.affinityKey != second.affinityKey {
		t.Fatalf("soft identity drifted across turns: first=%#v second=%#v", first, second)
	}
	// 无 system/tools 时，不同首条用户内容仍必须隔离
	// Different first user messages must remain isolated.
	other := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", []byte(`{"messages":[{"role":"user","content":"different"}]}`))
	if other.upstreamID == first.upstreamID {
		t.Fatal("different first user shared soft upstream id")
	}
}

func TestResolveBuildSessionIdentitySoftSurvivesHistoryTruncation(t *testing.T) {
	// Claude Code / Codex 长会话会截掉最早 user，但 system + tools 保持稳定。
	// 旧逻辑把 firstUser 编入身份，截断后 conv-id 漂移，缓存率接近 0。
	turn1 := []byte(`{
		"system":"stable agent rules",
		"tools":[{"name":"Read"},{"name":"Bash"},{"type":"function","function":{"name":"Edit"}}],
		"messages":[{"role":"user","content":"first question about mutex"}]
	}`)
	// 截断后最早 user 消失，只剩较新的对话
	turnN := []byte(`{
		"system":"stable agent rules",
		"tools":[{"type":"function","function":{"name":"Edit"}},{"name":"Bash"},{"name":"Read"}],
		"messages":[
			{"role":"user","content":"later question after truncation"},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":"continue"}
		]
	}`)
	id1 := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", turn1)
	idN := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", turnN)
	if !id1.soft || id1.upstreamID == "" {
		t.Fatalf("expected soft prefix identity, got %#v", id1)
	}
	if id1.upstreamID != idN.upstreamID || id1.affinityKey != idN.affinityKey {
		t.Fatalf("soft identity drifted after history truncation: %#v vs %#v", id1, idN)
	}
	// tools 集合变化应隔离，避免不同 Agent 配置互相污染
	changedTools := []byte(`{
		"system":"stable agent rules",
		"tools":[{"name":"Read"},{"name":"Write"}],
		"messages":[{"role":"user","content":"later question after truncation"}]
	}`)
	other := resolveBuildSessionIdentity(7, accountdomain.ProviderBuild, "grok-4.5", "", "", changedTools)
	if other.upstreamID == id1.upstreamID {
		t.Fatal("different tools fingerprint must isolate soft session")
	}
}

func TestRebuildBuildAffinityKeyIsStableAndModelScoped(t *testing.T) {
	keyA := rebuildBuildAffinityKey(9, accountdomain.ProviderBuild, "grok-4.5", "session-uuid")
	keyB := rebuildBuildAffinityKey(9, accountdomain.ProviderBuild, "grok-4.5", "session-uuid")
	keyOtherModel := rebuildBuildAffinityKey(9, accountdomain.ProviderBuild, "grok-4.3", "session-uuid")
	if keyA == "" || keyA != keyB {
		t.Fatalf("affinity restore unstable: %q %q", keyA, keyB)
	}
	if keyA == keyOtherModel {
		t.Fatal("affinity restore must be model-scoped")
	}
	if rebuildBuildAffinityKey(0, accountdomain.ProviderBuild, "grok-4.5", "session-uuid") != "" {
		t.Fatal("missing client ownership must not invent affinity")
	}
}

func TestResolveBuildSessionIdentitySoftFromResponsesInput(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"cache me"}]}]}`)
	first := resolveBuildSessionIdentity(9, accountdomain.ProviderBuild, "grok-4.5", "", "", body)
	second := resolveBuildSessionIdentity(9, accountdomain.ProviderBuild, "grok-4.5", "", "", body)
	if first.upstreamID == "" || first != second || !first.soft {
		t.Fatalf("responses soft identity unstable: %#v %#v", first, second)
	}
	if first.replayKey != "" {
		t.Fatalf("responses soft identity must not enable reasoning replay: %#v", first)
	}
}

func TestResolveBuildSessionIdentityUsesInstructionsAsSystemAnchor(t *testing.T) {
	// Responses commonly uses top-level instructions instead of messages[system].
	a := resolveBuildSessionIdentity(3, accountdomain.ProviderBuild, "grok-4.5", "", "", []byte(`{"instructions":"stable system","input":[{"role":"user","content":"hello"}]}`))
	b := resolveBuildSessionIdentity(3, accountdomain.ProviderBuild, "grok-4.5", "", "", []byte(`{"instructions":"stable system","input":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`))
	c := resolveBuildSessionIdentity(3, accountdomain.ProviderBuild, "grok-4.5", "", "", []byte(`{"instructions":"other system","input":[{"role":"user","content":"hello"}]}`))
	if !a.soft || a.upstreamID == "" || a.upstreamID != b.upstreamID {
		t.Fatalf("instructions soft session unstable: %#v %#v", a, b)
	}
	if a.upstreamID == c.upstreamID {
		t.Fatal("different instructions should isolate soft session")
	}
}
