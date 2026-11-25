//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestOpenAIDefaultReasoningEffort_HTTPChannelMapping 验证真实 HTTP 渠道改写不丢失原模型策略或等级后缀。
func TestOpenAIDefaultReasoningEffort_HTTPChannelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"responses", "chat/completions", "messages"} {
		for _, tc := range []struct {
			name       string // 场景名称。
			model      string // 渠道改写前的客户端模型。
			mappedTo   string // 原模型范围规则对 high 的映射目标。
			wantEffort string // 实际上游应收到的等级。
			denied     bool   // 是否应在访问上游前拒绝。
		}{
			{name: "scoped_mapping", model: "public", mappedTo: "low", wantEffort: "low"},
			{name: "scoped_deny", model: "public", mappedTo: service.ReasoningEffortMappingDeny, denied: true},
			{name: "model_suffix", model: "gpt-6.2-astra-low", wantEffort: "low"},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				const upstreamModel = "gpt-6.2-astra"
				groupID := int64(3132)
				group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Hydrated: true, Status: service.StatusActive, AllowMessagesDispatch: true}
				if tc.mappedTo != "" {
					group.ReasoningEffortMappings = []service.ReasoningEffortMapping{{From: "high", To: tc.mappedTo, MatchType: domain.ReasoningEffortMatchExact, Model: tc.model}}
				}
				channel := service.NewChannelService(&openAIWSUsageHandlerChannelRepoStub{
					channels: []service.Channel{{
						ID: 7701, Name: "reasoning-channel", Status: service.StatusActive, GroupIDs: []int64{groupID},
						ModelMapping: map[string]map[string]string{service.PlatformOpenAI: {tc.model: upstreamModel}},
					}},
					groupPlatforms: map[int64]string{groupID: service.PlatformOpenAI},
				}, nil, nil, nil, nil)
				account := service.Account{
					ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "test-key"},
					Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
				}
				upstream := newAstraProCapturedUpstream(&http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_channel\",\"model\":\"gpt-6.2-astra\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")),
				})
				usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				gateway := service.NewOpenAIGatewayService(
					&openAIWSUsageHandlerAccountRepoStub{account: account}, usageRepo, nil, nil, nil, nil, nil, cfg,
					nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{},
					nil, nil, nil, channel, nil, nil, nil,
				)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				body := fmt.Sprintf(`{"model":%q,"stream":true,"input":"hello"}`, tc.model)
				if endpoint != "responses" {
					body = fmt.Sprintf(`{"model":%q,"stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, tc.model)
				}
				c, rec := newAstraProFailoverContext(t, body)
				c.Request.URL.Path = "/v1/" + endpoint
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
				key, ok := middleware.GetAPIKeyFromContext(c)
				require.True(t, ok)
				key.Group = group
				switch endpoint {
				case "responses":
					h.Responses(c)
				case "chat/completions":
					h.ChatCompletions(c)
				case "messages":
					h.Messages(c)
				}
				_, _, bodies := upstream.snapshot()
				if tc.denied {
					require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					require.Empty(t, bodies)
					return
				}
				// 同时检查模型已改写和实际出站等级，避免仅在解析层保留原模型而遗漏下发。
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Len(t, bodies, 1)
				require.Equal(t, upstreamModel, gjson.GetBytes(bodies[0], "model").String())
				require.Equal(t, tc.wantEffort, gjson.GetBytes(bodies[0], "reasoning.effort").String())
			})
		}
	}
}
