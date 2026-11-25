package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestOpenAIDefaultReasoningEffort_HTTPForwarding 验证各 HTTP 出站路径在模型映射后使用默认等级。
func TestOpenAIDefaultReasoningEffort_HTTPForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses_native", "responses_passthrough", "chat_responses", "chat_oauth", "chat_responses_shape", "chat_raw", "messages_responses", "messages_oauth", "messages_raw"} {
		for _, tc := range []struct {
			name     string                   // 场景名称。
			fields   string                   // 客户端声明的推理字段。
			max      string                   // 分组允许的最高等级。
			mappings []ReasoningEffortMapping // 分组模型范围映射。
			want     string                   // 预期出站等级。
		}{
			{name: "omitted", want: "high"},
			{name: "null", fields: `null`, want: "high"},
			{name: "empty", fields: `"  "`, want: "high"},
			{name: "explicit", fields: `"low"`, want: "low"},
			{name: "group_ceiling", max: "medium", want: "medium"},
			{name: "group_scoped_mapping", mappings: []ReasoningEffortMapping{{From: "high", To: "low", MatchType: domain.ReasoningEffortMatchExact, Model: "public"}}, want: "low"},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				account := structuredInputRetryTestAccount()
				account.Credentials["model_mapping"] = map[string]any{"public": "gpt-6.2-astra"}
				if route == "responses_native" || route == "chat_oauth" || route == "messages_oauth" {
					account.Type = AccountTypeOAuth
					account.Credentials["access_token"] = "oauth-test"
					account.Extra["openai_passthrough"] = false
					account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
				}
				if route == "responses_passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				if route == "chat_raw" || route == "messages_raw" {
					account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
				}
				path := "/v1/responses"
				body := `{"model":"public","stream":true,"input":"hi"}`
				fieldPath := "reasoning.effort"
				if strings.HasPrefix(route, "chat_") {
					path = "/v1/chat/completions"
					if route != "chat_responses_shape" {
						body = `{"model":"public","stream":true,"messages":[{"role":"user","content":"hi"}]}`
						fieldPath = "reasoning_effort"
					}
				}
				if strings.HasPrefix(route, "messages_") {
					path = "/v1/messages"
					body = `{"model":"public","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
					fieldPath = "output_config.effort"
				}
				if tc.fields != "" {
					if fieldPath == "reasoning_effort" {
						body = strings.TrimSuffix(body, "}") + `,"reasoning_effort":` + tc.fields + `}`
					} else if fieldPath == "output_config.effort" {
						body = strings.TrimSuffix(body, "}") + `,"output_config":{"effort":` + tc.fields + `}}`
					} else {
						body = strings.TrimSuffix(body, "}") + `,"reasoning":{"summary":"auto","effort":` + tc.fields + `}}`
					}
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_default\",\"model\":\"gpt-6.2-astra\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\ndata: [DONE]\n\n")),
				}}
				if route == "chat_raw" || route == "messages_raw" {
					upstream.resp.Body = io.NopCloser(strings.NewReader("data: {\"id\":\"chat_default\",\"model\":\"gpt-6.2-astra\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
				}
				svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
				ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
					ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
					MaxReasoningEffort: tc.max, ReasoningEffortMappings: tc.mappings,
				})
				if strings.HasPrefix(route, "messages_") {
					ctx = WithOpenAIReasoningEffortPolicyForMessages(ctx, []byte(body), tc.max, tc.mappings, "")
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
				var result *OpenAIForwardResult
				var err error
				if path == "/v1/chat/completions" {
					result, err = svc.ForwardAsChatCompletions(ctx, c, account, []byte(body), "", "")
				} else if path == "/v1/messages" {
					result, err = svc.ForwardAsAnthropic(ctx, c, account, []byte(body), "", "")
				} else {
					result, err = svc.Forward(ctx, c, account, []byte(body))
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1)
				outPath := "reasoning.effort"
				if route == "chat_raw" || route == "messages_raw" {
					outPath = "reasoning_effort"
				}
				require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, outPath).String())
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, tc.want, *result.ReasoningEffort)
			})
		}
	}
}

// TestOpenAIDefaultReasoningEffort_DenyBeforeDispatch 验证默认值不会绕过分组拒绝策略。
func TestOpenAIDefaultReasoningEffort_DenyBeforeDispatch(t *testing.T) {
	for _, mappingDeny := range []bool{false, true} {
		t.Run(fmt.Sprint(mappingDeny), func(t *testing.T) {
			group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, MaxReasoningEffort: "low", MaxReasoningEffortOverLimit: ReasoningEffortOverLimitDeny}
			if mappingDeny {
				group.MaxReasoningEffort = ""
				group.ReasoningEffortMappings = []ReasoningEffortMapping{{From: "high", To: ReasoningEffortMappingDeny}}
			}
			ctx := context.WithValue(context.Background(), ctxkey.Group, group)
			body := []byte(`{"model":"gpt-6.2-astra","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))).WithContext(ctx)
			upstream := &httpUpstreamRecorder{err: fmt.Errorf("unexpected upstream dispatch")}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
			_, err := svc.ForwardAsChatCompletions(ctx, c, structuredInputRetryTestAccount(), body, "", "")
			require.True(t, IsReasoningEffortPolicyDenied(err), "got %v", err)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Empty(t, upstream.requests)
		})
	}
}

// TestOpenAIDefaultReasoningEffort_WebSocketForwarding 验证模型别名及分组策略在各 WS 模式的实际出站帧生效。
func TestOpenAIDefaultReasoningEffort_WebSocketForwarding(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModeDedicated, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		for _, want := range []string{"high", "medium"} {
			t.Run(mode+"/"+want, func(t *testing.T) {
				cfg := structuredInputRetryTestConfig()
				cfg.Gateway.OpenAIWS = config.GatewayOpenAIWSConfig{
					Enabled: true, OAuthEnabled: true, APIKeyEnabled: true, ResponsesWebsocketsV2: true,
					ModeRouterV2Enabled: true, IngressModeDefault: OpenAIWSIngressModeCtxPool,
					MaxConnsPerAccount: 1, MaxIdlePerAccount: 1, QueueLimitPerConn: 8,
					DialTimeoutSeconds: 3, ReadTimeoutSeconds: 3, WriteTimeoutSeconds: 3,
				}
				event := []byte(`{"type":"response.completed","response":{"id":"resp_default_ws","model":"gpt-6.2-astra","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
				upstreamConn := &openAIWSCaptureConn{events: [][]byte{event}}
				dialer := &openAIWSCaptureDialer{conn: upstreamConn}
				pool := newOpenAIWSConnPool(cfg)
				defer pool.Close()
				pool.setClientDialerForTest(dialer)
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader("data: " + string(event) + "\n\n")),
				}}
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
					openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
					openaiWSPool: pool, openaiWSPassthroughDialer: dialer,
				}
				account := structuredInputRetryTestAccount()
				account.Credentials["model_mapping"] = map[string]any{"mapped": "gpt-6.2-astra"}
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
				hooks := &OpenAIWSIngressHooks{
					MapRequestModel: func(_ int, _ string) (string, error) { return "mapped", nil },
				}
				if want == "medium" {
					hooks.ReasoningEffortMappings = []ReasoningEffortMapping{{From: "high", To: "medium", MatchType: domain.ReasoningEffortMatchExact, Model: "public"}}
				}
				done := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						done <- err
						return
					}
					defer conn.CloseNow()
					readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
					_, first, err := conn.Read(readCtx)
					cancel()
					if err != nil {
						done <- err
						return
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = r
					done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "sk-test", first, hooks)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
				require.NoError(t, err)
				defer client.CloseNow()
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"public","input":"hi"}`)))
				_, response, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String())
				_ = client.Close(coderws.StatusNormalClosure, "done")
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("等待 WS 会话结束超时")
				}
				var outbound []byte
				if mode == OpenAIWSIngressModeHTTPBridge {
					outbound = upstream.lastBody
				} else {
					require.Len(t, upstreamConn.writes, 1)
					outbound = []byte(requestToJSONString(upstreamConn.writes[0]))
				}
				require.Equal(t, "gpt-6.2-astra", gjson.GetBytes(outbound, "model").String())
				require.Equal(t, want, gjson.GetBytes(outbound, "reasoning.effort").String())
			})
		}
	}
}

// TestOpenAIDefaultReasoningEffort_WebSocketFollowup 验证后续帧省略模型时仍使用会话模型与分组策略。
func TestOpenAIDefaultReasoningEffort_WebSocketFollowup(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			upstreamWrites := make(chan []byte, 2)
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for turn := 1; turn <= 2; turn++ {
					_, payload, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					upstreamWrites <- payload
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_followup_%d","model":"gpt-6.2-astra","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`, turn)
					if err := conn.Write(r.Context(), coderws.MessageText, []byte(event)); err != nil {
						return
					}
				}
				// 保持上游连接到客户端结束，避免提前关闭掩盖第二帧转发的完成结果。
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstreamServer.Close()
			cfg := structuredInputRetryTestConfig()
			cfg.Gateway.OpenAIWS = config.GatewayOpenAIWSConfig{
				Enabled: true, APIKeyEnabled: true, ResponsesWebsocketsV2: true, ModeRouterV2Enabled: true,
				IngressModeDefault: mode, MaxConnsPerAccount: 1, MaxIdlePerAccount: 1, QueueLimitPerConn: 8,
				DialTimeoutSeconds: 3, ReadTimeoutSeconds: 3, WriteTimeoutSeconds: 3,
			}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			svc := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPool: pool, toolCorrector: NewCodexToolCorrector()}
			account := structuredInputRetryTestAccount()
			account.Credentials["base_url"] = upstreamServer.URL
			account.Credentials["model_mapping"] = map[string]any{"public": "gpt-6.2-astra"}
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			hooks := &OpenAIWSIngressHooks{MaxReasoningEffort: "medium"}
			done := make(chan error, 1)
			gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer conn.CloseNow()
				_, first, err := conn.Read(r.Context())
				if err != nil {
					done <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "sk-test", first, hooks)
			}))
			defer gatewayServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gatewayServer.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			for i, frame := range []string{`{"type":"response.create","model":"public","input":"first"}`, `{"type":"response.create","input":"second"}`} {
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(frame)))
				_, response, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("resp_followup_%d", i+1), gjson.GetBytes(response, "response.id").String())
				select {
				case outbound := <-upstreamWrites:
					require.Equal(t, "gpt-6.2-astra", gjson.GetBytes(outbound, "model").String())
					require.Equal(t, "medium", gjson.GetBytes(outbound, "reasoning.effort").String())
				case <-ctx.Done():
					t.Fatal("未收到上游请求帧")
				}
			}
			_ = client.Close(coderws.StatusNormalClosure, "done")
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("等待 WS 会话结束超时")
			}
		})
	}
}
