package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIReasoningEffortDefaultPolicyContextKey 保存缺省等级使用的完整分组策略，避免兼容桥接的缺省过滤丢失规则。
type openAIReasoningEffortDefaultPolicyContextKey struct{}

// applyOpenAIReasoningEffortDefault 补齐 high 或渠道改写丢失的模型后缀等级；返回是否已补值并应用策略。
func applyOpenAIReasoningEffortDefault(ctx context.Context, account *Account, body []byte, upstreamModel, effortPath string, requestedModels ...string) ([]byte, bool, error) {
	if account == nil || account.Platform != PlatformOpenAI || len(body) == 0 {
		return body, false, nil
	}
	if effortPath == "output_config.effort" {
		if gjson.GetBytes(body, "thinking.type").String() == "disabled" {
			return body, false, nil
		}
		if output := gjson.GetBytes(body, "output_config"); output.Type != gjson.Null && !output.IsObject() {
			return body, false, nil
		}
	}
	model := canonicalizeOpenAIModelAliasSpelling(upstreamModel)
	if model == "" {
		model = strings.ToLower(lastOpenAIModelSegment(upstreamModel))
	}
	// chat-latest 别名不接受推理等级；仅按最终上游模型判断，OAuth 归一后的推理基名不受影响。
	if strings.HasSuffix(model, "-chat-latest") {
		return body, false, nil
	}
	if !isOpenAICodexReasoningGPTModel(model) && model != "codex-auto-review" &&
		model != "o1" && !strings.HasPrefix(model, "o1-20") && model != "o3" && !strings.HasPrefix(model, "o3-") && model != "o4-mini" && !strings.HasPrefix(model, "o4-mini-") {
		return body, false, nil
	}
	bodyModel := gjson.GetBytes(body, "model").String()
	// WS 使用逐帧原模型；HTTP 原模型在渠道改写前存入 context，不能被改写后的 body.model 取代。
	requestedModel := firstNonEmpty(requestedModels...)
	if requestedModel == "" && ctx != nil {
		requestedModel, _ = ctx.Value(ctxkey.Model).(string)
	}
	requestedModel = firstNonEmpty(requestedModel, bodyModel)
	// 请求体或最终模型仍带后缀时沿用既有转换；逐项 configuration_update 同样保留。
	if deriveOpenAIReasoningEffortFromModelCandidates([]string{bodyModel, upstreamModel}) != "" || latestResponsesInputEffort(body) != "" {
		return body, false, nil
	}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		value := gjson.GetBytes(body, path)
		if value.Type != gjson.Null && (value.Type != gjson.String || strings.TrimSpace(value.String()) != "") {
			return body, false, nil
		}
	}
	if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.Type != gjson.Null && !reasoning.IsObject() {
		return body, false, nil
	}
	// 旧模型的 mode=pro 会在后续规范化中派生 max，不能先用 high 阻断这一约定。
	if !isOpenAIGPT6Model(model) && strings.EqualFold(gjson.GetBytes(body, "reasoning.mode").String(), "pro") {
		return body, false, nil
	}

	// 渠道映射可能已剥掉显式后缀，必须将其写回协议字段，而非只跳过 high。
	effort := firstNonEmpty(deriveOpenAIReasoningEffortFromModel(requestedModel), "high")
	policy := openAIReasoningEffortPolicy{}
	if ctx != nil {
		if defaults, ok := ctx.Value(openAIReasoningEffortDefaultPolicyContextKey{}).(openAIReasoningEffortPolicy); ok {
			policy = defaults
		} else if bound, ok := ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy); ok {
			policy = bound
		} else if group, _ := ctx.Value(ctxkey.Group).(*Group); IsGroupContextValid(group) && (group.Platform == PlatformOpenAI || group.Platform == PlatformComposite) {
			policy = openAIReasoningEffortPolicy{maxEffort: group.MaxReasoningEffort, mappings: group.ReasoningEffortMappings, overLimit: group.MaxReasoningEffortOverLimit}
		}
	}
	// 只评估本次补齐的等级，避免再次映射已有显式等级；模型范围仍以客户端请求名匹配。
	policyBody, err := json.Marshal(map[string]string{"model": requestedModel, "reasoning_effort": effort})
	if err != nil {
		return body, false, fmt.Errorf("prepare default reasoning effort policy: %w", err)
	}
	policyBody, _, err = ApplyReasoningEffortPolicy(policyBody, policy.maxEffort, policy.mappings, policy.overLimit)
	if err != nil {
		return body, false, err
	}
	updated, err := sjson.SetBytes(body, effortPath, gjson.GetBytes(policyBody, "reasoning_effort").String())
	if err != nil {
		return body, false, fmt.Errorf("set default reasoning effort: %w", err)
	}
	return updated, true, nil
}
