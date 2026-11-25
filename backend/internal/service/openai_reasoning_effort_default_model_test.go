package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestOpenAIDefaultReasoningEffort_RequestModelSources 验证逐帧模型优先于 HTTP 原模型，原模型优先于渠道改写后的字段。
func TestOpenAIDefaultReasoningEffort_RequestModelSources(t *testing.T) {
	for _, tc := range []struct {
		name       string   // 场景名称。
		context    string   // HTTP 入口保存的原模型。
		frame      []string // WS 逐帧显式传入的原模型。
		wantEffort string   // 原模型范围规则命中后的等级。
	}{
		{name: "http_original_model", context: "public", wantEffort: "low"},
		{name: "frame_over_context", context: "stale", frame: []string{"public"}, wantEffort: "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxkey.Model, tc.context)
			ctx = WithOpenAIReasoningEffortPolicy(ctx, "", []ReasoningEffortMapping{{From: "high", To: "low", MatchType: domain.ReasoningEffortMatchExact, Model: "public"}}, "")
			body, _, err := applyOpenAIReasoningEffortDefault(ctx, structuredInputRetryTestAccount(), []byte(`{"model":"gpt-6.2-astra"}`), "gpt-6.2-astra", "reasoning.effort", tc.frame...)
			require.NoError(t, err)
			require.Equal(t, tc.wantEffort, gjson.GetBytes(body, "reasoning.effort").String())
		})
	}
}

// TestOpenAIDefaultReasoningEffort_ChatLatestModelCapability 验证 chat-latest 别名不支持默认等级，OAuth 归一到推理基名后仍补 high。
func TestOpenAIDefaultReasoningEffort_ChatLatestModelCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string // 场景名称。
		chat       bool   // 是否使用 Chat Completions 入站。
		oauth      bool   // 是否归一为 Codex 推理基名。
		wantModel  string // 最终实际上游模型。
		wantEffort string // 能力允许时才新增 high。
	}{
		{name: "apikey_responses", wantModel: "gpt-5.2-chat-latest"},
		{name: "apikey_raw_chat", chat: true, wantModel: "gpt-5.2-chat-latest"},
		{name: "oauth_responses", oauth: true, wantModel: "gpt-5.2", wantEffort: "high"},
		{name: "oauth_chat", chat: true, oauth: true, wantModel: "gpt-5.2", wantEffort: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := structuredInputRetryTestAccount()
			account.Credentials["model_mapping"] = map[string]any{"public": "gpt-5.2-chat-latest"}
			if tc.oauth {
				account.Type = AccountTypeOAuth
				account.Credentials["access_token"] = "oauth-test"
				account.Extra["openai_passthrough"] = false
				account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
			} else if tc.chat {
				account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
			}
			body := `{"model":"public","stream":true,"input":"hi"}`
			path := "/v1/responses"
			response := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_capability\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
			if tc.chat {
				body = `{"model":"public","stream":true,"messages":[{"role":"user","content":"hi"}]}`
				path = "/v1/chat/completions"
				if !tc.oauth {
					response = "data: {\"id\":\"chat_capability\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				}
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response))}}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			var result *OpenAIForwardResult
			var err error
			if tc.chat {
				result, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(body), "", "")
			} else {
				result, err = svc.Forward(c.Request.Context(), c, account, []byte(body))
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tc.wantModel, gjson.GetBytes(upstream.lastBody, "model").String())
			effortPath := "reasoning.effort"
			if tc.chat && !tc.oauth {
				effortPath = "reasoning_effort"
			}
			require.Equal(t, tc.wantEffort, gjson.GetBytes(upstream.lastBody, effortPath).String())
			if tc.wantEffort == "" {
				require.False(t, gjson.GetBytes(upstream.lastBody, effortPath).Exists())
			}
		})
	}
}
