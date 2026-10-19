package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
)

const ModelConsistencyResultsExtraKey = "model_consistency_results"
const modelConsistencyProfile = "codex-number-selection-v1"
const modelConsistencyMaxModels = 8
const modelConsistencyDeadline = 10 * time.Minute
const modelConsistencySampleTimeout = 45 * time.Second

// ModelConsistencyVersions 记录可复现的分类资源版本组合。
type ModelConsistencyVersions struct {
	Bank      string `json:"bank"`      // 固定指纹库提交版本。
	Algorithm string `json:"algorithm"` // 分类与有效性检查版本。
	Challenge string `json:"challenge"` // 挑战生成版本。
	Profile   string `json:"profile"`   // 请求参数组合版本。
}

// ModelConsistencyUsage 仅记录上游报告的 token 用量，不进入业务计费。
type ModelConsistencyUsage struct {
	InputTokens  int64 `json:"input_tokens"`  // 上游报告的输入 token 数。
	OutputTokens int64 `json:"output_tokens"` // 上游报告的输出 token 数。
}

// ModelConsistencyResult 定义管理员可见的诊断字段白名单。
type ModelConsistencyResult struct {
	RunID            string                   `json:"run_id"`                     // 本次运行标识。
	RequestedModel   string                   `json:"requested_model"`            // 管理员选择的请求模型。
	UpstreamModel    string                   `json:"upstream_model"`             // 应用账号映射后的实际上游模型。
	Status           string                   `json:"status"`                     // 运行状态或经验判断结果。
	Reason           string                   `json:"reason"`                     // 脱敏后的稳定原因码。
	StartedAt        time.Time                `json:"started_at"`                 // 本次运行开始时间。
	DeadlineAt       time.Time                `json:"deadline_at"`                // 最晚允许执行与完成写入的时间。
	CompletedAt      *time.Time               `json:"completed_at,omitempty"`     // 本次完成时间。
	Applicable       bool                     `json:"applicable"`                 // 当前配置是否仍与诊断配置一致。
	SampleCount      int                      `json:"sample_count"`               // 已尝试的独立样本数。
	ValidSampleCount int                      `json:"valid_sample_count"`         // 通过完整性和挑战校验的样本数。
	CandidateModel   string                   `json:"candidate_model,omitempty"`  // 三份有效样本一致的参考候选。
	Score            *float64                 `json:"score,omitempty"`            // 候选的闭集相对分数，不是准确率。
	Transport        string                   `json:"transport"`                  // 实际选择的 HTTP 或 WebSocket 传输。
	TransportReason  string                   `json:"transport_reason,omitempty"` // 传输选择或受限原因。
	Profile          string                   `json:"profile"`                    // 固定请求 profile。
	Versions         ModelConsistencyVersions `json:"versions"`                   // 固定版本组合。
	Usage            *ModelConsistencyUsage   `json:"usage,omitempty"`            // 可取得的上游实际用量。
	Limitation       string                   `json:"limitation,omitempty"`       // 经验判断的可比性限制。

	UpstreamStatusCode int `json:"upstream_status_code,omitempty"` // 上游 HTTP 请求或 WS 握手失败的状态码。
}

// ModelConsistencySnapshot 保存服务端托管结果及条件更新所需的配置摘要。
type ModelConsistencySnapshot struct {
	ModelConsistencyResult        // 可向管理员展示的脱敏结果。
	ConfigDigest           string `json:"config_digest"` // 含凭据与代理配置的不可逆摘要。
}

// ModelConsistencyView 返回账号支持状态和最近一次诊断结果。
type ModelConsistencyView struct {
	Supported         bool                     `json:"supported"`                    // 是否支持当前账号类型。
	UnsupportedReason string                   `json:"unsupported_reason,omitempty"` // 不支持时的稳定原因码。
	ReferenceModels   []string                 `json:"reference_models"`             // 固定指纹库覆盖的模型。
	Results           []ModelConsistencyResult `json:"results"`                      // 各请求模型最近启动的一次运行。
}

// ModelConsistencyRun 返回启动后的运行标识和初始快照。
type ModelConsistencyRun struct {
	RunID   string                   `json:"run_id"`  // 供跨实例取消使用的运行标识。
	Results []ModelConsistencyResult `json:"results"` // 已持久化的初始结果。
}

// isModelConsistencyAccount 限定普通 OpenAI OAuth 和 SetupToken 账号。
func isModelConsistencyAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformOpenAI || (account.Type != AccountTypeOAuth && account.Type != AccountTypeSetupToken) || account.IsOpenAIAgentIdentity() {
		return false
	}
	for _, key := range []string{openAIAuthModeCredentialKey, openAIAuthModeLegacyCredentialKey} {
		mode := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(account.GetCredential(key))), "_", "")
		if mode == "agentidentity" {
			return false
		}
	}
	return true
}

// StripModelConsistencyExtra 阻止通用账号写入伪造服务端托管结果。
func StripModelConsistencyExtra(extra map[string]any) map[string]any {
	if _, found := extra[ModelConsistencyResultsExtraKey]; !found {
		return extra
	}
	clean := make(map[string]any, len(extra)-1)
	for key, value := range extra {
		if key != ModelConsistencyResultsExtraKey {
			clean[key] = value
		}
	}
	return clean
}

// modelConsistencySnapshots 从托管字段解码已知字段，避免展示未知内容。
func modelConsistencySnapshots(account *Account) (map[string]ModelConsistencySnapshot, error) {
	results := make(map[string]ModelConsistencySnapshot)
	if account == nil || account.Extra[ModelConsistencyResultsExtraKey] == nil {
		return results, nil
	}
	data, err := json.Marshal(account.Extra[ModelConsistencyResultsExtraKey])
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// modelConsistencyConfigDigest 仅对会影响诊断语义的配置生成摘要。
func modelConsistencyConfigDigest(account *Account, decision OpenAIWSProtocolDecision, plugin any) (string, error) {
	extra := make(map[string]any)
	for key, value := range account.Extra {
		if key == ModelConsistencyResultsExtraKey || strings.HasPrefix(key, "codex_primary_") || strings.HasPrefix(key, "codex_secondary_") || strings.HasPrefix(key, "codex_5h_") || strings.HasPrefix(key, "codex_7d_") || strings.HasPrefix(key, "codex_reset_credit_") || key == "codex_usage_updated_at" || key == "codex_credits_snapshot" || key == "codex_referral_snapshot" || strings.HasPrefix(key, "passive_usage_") || strings.HasPrefix(key, "upstream_billing_") {
			continue
		}
		extra[key] = value
	}
	var proxy any
	if account.Proxy != nil {
		proxy = []any{account.Proxy.ID, account.Proxy.URL(), account.Proxy.Status, account.Proxy.ExpiresAt, account.Proxy.UpdatedAt}
	}
	data, err := json.Marshal([]any{account.Platform, account.Type, account.Credentials, extra, account.ProxyID, proxy, decision, plugin, modelConsistencyProfile, modeltrace.ModelTraceBankCommit(), modeltrace.AlgorithmVersion, modeltrace.ChallengeVersion})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// modelConsistencyOrderedResults 对结果稳定排序，避免页面刷新时顺序跳变。
func modelConsistencyOrderedResults(results map[string]ModelConsistencySnapshot) []ModelConsistencyResult {
	list := make([]ModelConsistencyResult, 0, len(results))
	for _, snapshot := range results {
		list = append(list, snapshot.ModelConsistencyResult)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RequestedModel < list[j].RequestedModel })
	return list
}
