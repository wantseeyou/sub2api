package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// withCLIVersionResolverForTest 注入解析器并在用例结束时还原（置 nil），避免用例间污染。
func withCLIVersionResolverForTest(t *testing.T, resolver func() string) {
	t.Helper()
	claude.SetCLIVersionResolver(resolver)
	t.Cleanup(func() { claude.SetCLIVersionResolver(nil) })
}

// 后台注入更高版本时，两条 OAuth 出站路径的 UA 与 billing 仍必须使用固定版本。
func TestBuildOAuthMimicRequest_RuntimeVersionConsistentBetweenHeaderAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upgraded = "9.9.9"
	withCLIVersionResolverForTest(t, func() string { return upgraded })

	for _, endpoint := range []string{"messages", "count_tokens"} {
		t.Run(endpoint, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
			billing, err := buildBillingAttributionText(body, "2.1.81")
			require.NoError(t, err)
			body, err = sjson.SetBytes(body, "system.0.text", billing)
			require.NoError(t, err)

			svc := &GatewayService{cfg: &config.Config{}}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

			var req *http.Request
			var wireBody []byte
			if endpoint == "messages" {
				req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
					body, "test-token", "oauth", "claude-haiku-4-5", false, true)
			} else {
				req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
					body, "test-token", "oauth", "claude-haiku-4-5", true)
			}
			require.NoError(t, err)

			wantUA := "claude-cli/" + claude.CLICurrentVersion + " (external, cli)"
			outboundUA := getHeaderRaw(req.Header, "User-Agent")
			require.Equal(t, wantUA, outboundUA)

			billingText := gjson.GetBytes(wireBody, "system.0.text").String()
			require.Contains(t, billingText, "x-anthropic-billing-header")
			require.Contains(t, billingText, "cc_version="+claude.CLICurrentVersion+"."+computeClaudeCodeFingerprint(wireBody, claude.CLICurrentVersion)+";")
			require.NotContains(t, billingText, "cc_version=2.1.81")

			// 头/体版本号必须来自同一字符串。
			headerVersion := ExtractCLIVersion(outboundUA)
			require.Equal(t, claude.CLICurrentVersion, headerVersion)
			require.Contains(t, billingText, "cc_version="+headerVersion+".")
		})
	}
}

// 后台更高版本不改变任何已有缓存版本的迁移目标。
func TestGetOrCreateFingerprintIgnoresRuntimeVersion(t *testing.T) {
	withCLIVersionResolverForTest(t, func() string { return "9.9.9" })
	for _, ua := range []string{"claude-cli/2.1.100 (external, cli)", "claude-cli/9.9.9 (external, cli)", "claude-cli/99.0.0 (external, cli)", "claude-cli/1.0.0 (external, cli)"} {
		cache := &stubIdentityCache{fingerprint: &Fingerprint{UserAgent: ua, ClientID: "cid-1"}}
		fp, err := NewIdentityService(cache).GetOrCreateFingerprint(context.Background(), 1, nil)
		require.NoError(t, err)
		require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)
		require.Equal(t, "cid-1", fp.ClientID)
	}
}

// 后台更高版本不扩大可接受的客户端环境。
func TestFingerprintIgnoresRuntimeAndClientVersions(t *testing.T) {
	withCLIVersionResolverForTest(t, func() string { return "9.9.9" })
	for _, ua := range []string{"claude-cli/9.9.9 (external, cli)", "claude-cli/11.0.0 (external, cli)", "claude-cli/12.0.0 (external, cli)"} {
		fp, err := NewIdentityService(&stubIdentityCache{}).GetOrCreateFingerprint(context.Background(), 1, headersWithUA(ua))
		require.NoError(t, err)
		require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)
	}
}

// 默认指纹与运行期解析器无关，始终取固定版本。
func TestDefaultFingerprintIgnoresRuntimeVersion(t *testing.T) {
	withCLIVersionResolverForTest(t, func() string { return "9.9.9" })
	require.Equal(t, "claude-cli/"+claude.CLICurrentVersion+" (external, cli)", defaultFingerprint().UserAgent)
}
