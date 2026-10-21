package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// 旧版本、超前版本、其他产品及畸形缓存均迁移到同一固定环境。
func TestGetOrCreateFingerprintPinsCachedUserAgent(t *testing.T) {
	for _, ua := range []string{
		"claude-cli/2.1.220 (external, cli)",
		"claude-cli/2.1.100 (external, claude-desktop-3p, agent-sdk/0.3.100)",
		claude.DefaultUserAgent(),
		"claude-cli/2.9.0 (external, cli)",
		"opencode/1.2.3 (external, cli)",
		"",
		"claude-cli (external, cli)",
		"claude-cli/abc (external, cli)",
	} {
		t.Run(ua, func(t *testing.T) {
			cache := &stubIdentityCache{fingerprint: &Fingerprint{UserAgent: ua, ClientID: "cid-1"}}
			fp, err := NewIdentityService(cache).GetOrCreateFingerprint(context.Background(), 1, nil)
			require.NoError(t, err)
			require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)
			require.Equal(t, "cid-1", fp.ClientID)
		})
	}
}

// GetOrCreateFingerprint 集成行为，直接对应线上故障：缓存指纹停留在历史版本
// 2.1.220（生产 Redis 中账号 147 的实际值），客户端送来更旧的 2.1.75。
// 修复前：isNewerVersion 不触发、UA 形态合法不触发自愈，旧指纹被原样返回并
// 近乎永不过期——上游按指纹 UA 做客户端版本闸门（Fable 5.1 要求 >= 2.1.251），
// 仅升 CLICurrentVersion 对存量账号完全无效。
func TestGetOrCreateFingerprintFloorsStaleCachedUserAgent(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent:               "claude-cli/2.1.220 (external, cli)",
		ClientID:                "cid-1",
		StainlessPackageVersion: "0.91.1",
		UpdatedAt:               time.Now().Unix(),
	}}
	svc := NewIdentityService(cache)

	fp, err := svc.GetOrCreateFingerprint(
		context.Background(), 147,
		headersWithUA("claude-cli/2.1.75 (external, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, "claude-cli/"+claude.CLICurrentVersion+" (external, cli)", fp.UserAgent)
	require.Equal(t, 1, cache.setCalls, "下限抬升必须持久化写回缓存")
	require.Equal(t, "claude-cli/"+claude.CLICurrentVersion+" (external, cli)", cache.lastSet.UserAgent)
	// 迁移时 SDK 与运行时一起更新，不拼接旧缓存环境。
	require.Equal(t, "0.128.0", fp.StainlessPackageVersion)
	require.Equal(t, "v26.3.0", fp.StainlessRuntimeVersion)
	require.Equal(t, "cid-1", fp.ClientID)
}

// 旧缓存中的超前版本也迁移到固定环境。
func TestGetOrCreateFingerprintPinsCacheAboveBaseline(t *testing.T) {
	ua := "claude-cli/2.9.0 (external, cli)"
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent:               ua,
		ClientID:                "cid-1",
		StainlessPackageVersion: "0.91.1",
		UpdatedAt:               time.Now().Unix(),
	}}
	svc := NewIdentityService(cache)

	fp, err := svc.GetOrCreateFingerprint(
		context.Background(), 1,
		headersWithUA("claude-cli/2.1.75 (external, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}

// 客户端送来更新版本也不能改变固定环境。
func TestGetOrCreateFingerprintNewerClientCannotChangePin(t *testing.T) {
	newUA := "claude-cli/2.9.0 (external, cli)"
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/2.1.220 (external, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	svc := NewIdentityService(cache)

	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, headersWithUA(newUA))

	require.NoError(t, err)
	require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}

// 客户端版本高于缓存但低于下限：merge 升级后仍被抬到下限（二者取更新者）。
func TestGetOrCreateFingerprintFloorWinsOverStaleClientUpgrade(t *testing.T) {
	cache := &stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/2.1.22 (external, cli)",
		ClientID:  "cid-1",
		UpdatedAt: time.Now().Unix(),
	}}
	svc := NewIdentityService(cache)

	fp, err := svc.GetOrCreateFingerprint(
		context.Background(), 1,
		headersWithUA("claude-cli/2.1.223 (external, cli)"),
	)

	require.NoError(t, err)
	require.Equal(t, "claude-cli/"+claude.CLICurrentVersion+" (external, cli)", fp.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}

// 首次创建路径同样受下限约束：合法但过旧的 claude-cli UA 落库时必须抬到 CLICurrentVersion。
func TestCreateFingerprintFromHeadersFloorsOldClientUserAgent(t *testing.T) {
	cache := &stubIdentityCache{}
	svc := NewIdentityService(cache)

	fp, err := svc.GetOrCreateFingerprint(
		context.Background(), 1,
		headersWithUA("claude-cli/2.1.75 (external, claude-desktop-3p)"),
	)

	require.NoError(t, err)
	require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}
