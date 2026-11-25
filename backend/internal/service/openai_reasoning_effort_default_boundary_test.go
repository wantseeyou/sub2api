package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestOpenAIDefaultReasoningEffort_PreservesSelections 验证显式选择、配置更新及非推理模型不被默认值覆盖。
func TestOpenAIDefaultReasoningEffort_PreservesSelections(t *testing.T) {
	for _, tc := range []struct {
		name     string // 场景名称。
		model    string // 实际上游模型。
		body     string // 客户端原始请求。
		platform string // 非空时覆盖账号平台。
	}{
		{name: "nested_none", model: "gpt-6.2-astra", body: `{"model":"public","reasoning":{"effort":"none"}}`},
		{name: "flat_none", model: "gpt-6.2-astra", body: `{"model":"public","reasoning_effort":"none"}`},
		{name: "explicit_minimal", model: "gpt-6.2-astra", body: `{"model":"public","reasoning_effort":"minimal"}`},
		{name: "explicit_max", model: "gpt-6.2-astra", body: `{"model":"public","reasoning_effort":"max"}`},
		{name: "unknown_effort", model: "gpt-6.2-astra", body: `{"model":"public","reasoning_effort":"future"}`},
		{name: "invalid_effort_type", model: "gpt-6.2-astra", body: `{"model":"public","reasoning_effort":false}`},
		{name: "invalid_reasoning_type", model: "gpt-6.2-astra", body: `{"model":"public","reasoning":[]}`},
		{name: "model_effort_suffix", model: "gpt-6.2-astra", body: `{"model":"gpt-6.2-astra-low"}`},
		{name: "model_none_suffix", model: "gpt-6.2-astra", body: `{"model":"gpt-6.2-astra-none"}`},
		{name: "input_effort", model: "gpt-6.2-astra", body: `{"model":"public","input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`},
		{name: "legacy_pro_mode", model: "gpt-5.5", body: `{"model":"public","reasoning":{"mode":"pro"}}`},
		{name: "gpt4o", model: "gpt-4o", body: `{"model":"public","input":"hi"}`},
		{name: "o1_mini", model: "o1-mini", body: `{"model":"public","input":"hi"}`},
		{name: "deepseek", model: "deepseek-v4-pro", body: `{"model":"gpt-6.2-astra","input":"hi"}`},
		{name: "glm", model: "glm-5.2", body: `{"model":"public","input":"hi"}`},
		{name: "claude", model: "claude-opus-5-5", body: `{"model":"public","input":"hi"}`},
		{name: "custom", model: "company-coding-model", body: `{"model":"public","input":"hi"}`},
		{name: "other_platform", model: "gpt-6.2-astra", body: `{"model":"public","input":"hi"}`, platform: PlatformDeepseek},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := structuredInputRetryTestAccount()
			if tc.platform != "" {
				account.Platform = tc.platform
			}
			ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "low", []ReasoningEffortMapping{{From: "none", To: ReasoningEffortMappingDeny}}, "")
			got, _, err := applyOpenAIReasoningEffortDefault(ctx, account, []byte(tc.body), tc.model, "reasoning.effort")
			require.NoError(t, err)
			require.Equal(t, tc.body, string(got))
		})
	}
}

// TestOpenAIDefaultReasoningEffort_OnlyMapsDefaultOnce 验证重试及多层转发不会重复执行等级映射。
func TestOpenAIDefaultReasoningEffort_OnlyMapsDefaultOnce(t *testing.T) {
	ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "", []ReasoningEffortMapping{{From: "high", To: "medium"}, {From: "medium", To: "low"}}, "")
	account := structuredInputRetryTestAccount()
	body := []byte(`{"model":"gpt-6.2-astra","reasoning":{"summary":"auto"}}`)
	first, _, err := applyOpenAIReasoningEffortDefault(ctx, account, body, "gpt-6.2-astra", "reasoning.effort")
	require.NoError(t, err)
	require.Equal(t, "medium", gjson.GetBytes(first, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(first, "reasoning.summary").String())
	second, _, err := applyOpenAIReasoningEffortDefault(ctx, account, first, "gpt-6.2-astra", "reasoning.effort")
	require.NoError(t, err)
	require.Equal(t, first, second)
}
