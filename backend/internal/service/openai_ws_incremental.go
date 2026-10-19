package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 每条连接最多保留 64 KiB 的历史摘要，超过上限的请求继续全量发送。
const openAIWSIncrementalMaxItems = 2048
const openAIWSIncrementalAnchorMissing = "incremental_previous_response_not_found"
const openAIWSIncrementalRecoveryTriedKey = "openai_ws_incremental_recovery_tried"

// openAIWSIncrementalState 只保存最近一次成功响应的上下文摘要，不保存对话正文。
type openAIWSIncrementalState struct {
	owner      [sha256.Size]byte   // 调用方、分组、账号、线程及上下文窗口的身份摘要。
	parameters [sha256.Size]byte   // 除输入和传输元数据外的请求参数摘要。
	items      [][sha256.Size]byte // 完整输入及上游输出的逐项摘要。
	responseID string              // 本连接最近一次完整成功响应的续接标识。
}

// openAIWSIncrementalOwner 仅为有明确调用方和会话标识的普通 Codex 账号生成隔离键。
func openAIWSIncrementalOwner(c *gin.Context, account *Account, scope string) [sha256.Size]byte {
	callerID := getAPIKeyIDFromContext(c)
	if account == nil || !account.IsOpenAIOAuthLike() || account.IsOpenAIAgentIdentity() || callerID <= 0 || strings.TrimSpace(scope) == "" {
		return [sha256.Size]byte{}
	}
	identity := currentCodexClientIdentity(c)
	return sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%s:%s", callerID, getOpenAIGroupIDFromContext(c), account.ID, scope, identity.windowID)))
}

// prepareOpenAIWSIncremental 仅在同连接的完整历史前缀和参数均匹配时裁掉已确认的上下文。
func prepareOpenAIWSIncremental(lease *openAIWSConnLease, owner [sha256.Size]byte, payload []byte) ([]byte, *openAIWSIncrementalState, int) {
	if lease == nil || lease.conn == nil || owner == ([sha256.Size]byte{}) || gjson.GetBytes(payload, "previous_response_id").Exists() || gjson.GetBytes(payload, "generate").Raw == "false" {
		return payload, nil, 0
	}
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload, nil, 0
	}
	items := input.Array()
	if len(items) == 0 || len(items) > openAIWSIncrementalMaxItems {
		return payload, nil, 0
	}
	parameters, err := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
	if err != nil {
		return payload, nil, 0
	}
	next := &openAIWSIncrementalState{owner: owner, parameters: sha256.Sum256(parameters), items: make([][sha256.Size]byte, len(items))}
	for i, item := range items {
		next.items[i] = sha256.Sum256(normalizeOpenAIWSReplayItemForCompare([]byte(item.Raw)))
	}
	lease.conn.writeMu.Lock()
	previous := lease.conn.incrementalState
	lease.conn.writeMu.Unlock()
	if previous == nil || previous.owner != owner || previous.parameters != next.parameters || previous.responseID == "" || len(previous.items) >= len(next.items) {
		return payload, next, 0
	}
	for i, digest := range previous.items {
		if next.items[i] != digest {
			return payload, next, 0
		}
	}
	delta := make([]json.RawMessage, 0, len(items)-len(previous.items))
	for _, item := range items[len(previous.items):] {
		delta = append(delta, json.RawMessage(item.Raw))
	}
	encoded, err := json.Marshal(delta)
	if err != nil {
		return payload, next, 0
	}
	wire, err := sjson.SetRawBytes(payload, "input", encoded)
	if err != nil {
		return payload, next, 0
	}
	wire, err = sjson.SetBytes(wire, "previous_response_id", previous.responseID)
	if err != nil {
		return payload, next, 0
	}
	return wire, next, len(previous.items)
}

// completeOpenAIWSIncremental 仅在完整成功终态后登记上游原始输出，失败与未完成响应不建立续链。
func completeOpenAIWSIncremental(lease *openAIWSConnLease, next *openAIWSIncrementalState, event []byte) {
	if next == nil || lease == nil || lease.conn == nil || gjson.GetBytes(event, "type").String() != "response.completed" || gjson.GetBytes(event, "response.status").String() != "completed" {
		return
	}
	responseID := strings.TrimSpace(gjson.GetBytes(event, "response.id").String())
	output := gjson.GetBytes(event, "response.output")
	if responseID == "" || !output.IsArray() {
		return
	}
	items := output.Array()
	if len(items) == 0 || len(next.items)+len(items) > openAIWSIncrementalMaxItems {
		return
	}
	digests := make([][sha256.Size]byte, len(next.items)+len(items))
	copy(digests, next.items)
	for i, item := range items {
		digests[len(next.items)+i] = sha256.Sum256(normalizeOpenAIWSReplayItemForCompare([]byte(item.Raw)))
	}
	next.items = digests
	next.responseID = responseID
	lease.conn.writeMu.Lock()
	lease.conn.incrementalState = next
	lease.conn.writeMu.Unlock()
}
