package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// fingerprintSalt 来自官方 Claude Code 2.1.299 的 billing 后缀算法。
const fingerprintSalt = "59cf53e54c78"

// computeClaudeCodeFingerprint 按官方 JavaScript 的 UTF-16 下标计算 cc_version 后缀。
func computeClaudeCodeFingerprint(body []byte, version string) string {
	return computeClaudeCodeFingerprintFromText(extractFirstUserText(body), version)
}

// computeClaudeCodeFingerprintFromText 对取样后的 UTF-16 码元先拼接，再按 JavaScript 规则编码成 UTF-8。
func computeClaudeCodeFingerprintFromText(firstText, version string) string {
	var units [3]uint16
	for n, index := range [...]int{4, 7, 20} {
		unit, ok := javascriptUTF16CodeUnitAt(firstText, index)
		if !ok {
			unit = '0'
		}
		units[n] = unit
	}
	input := make([]byte, 0, len(fingerprintSalt)+len(version)+12)
	input = append(input, fingerprintSalt...)
	for i := 0; i < len(units); i++ {
		r := rune(units[i])
		if r >= 0xD800 && r <= 0xDBFF && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] <= 0xDFFF {
			r = utf16.DecodeRune(r, rune(units[i+1]))
			i++
		} else if utf16.IsSurrogate(r) {
			r = utf8.RuneError
		}
		input = utf8.AppendRune(input, r)
	}
	input = append(input, version...)
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])[:3]
}

// javascriptUTF16CodeUnitAt 读取 JavaScript 字符串下标对应的码元，无需转换整段长文本。
func javascriptUTF16CodeUnitAt(text string, index int) (uint16, bool) {
	position := 0
	for _, r := range text {
		if r <= 0xFFFF {
			if position == index {
				return uint16(r), true
			}
			position++
			continue
		}
		high, low := utf16.EncodeRune(r)
		if position == index {
			return uint16(high), true
		}
		if position+1 == index {
			return uint16(low), true
		}
		position += 2
	}
	return 0, false
}

// extractFirstUserText 提取 messages 中第一条非 meta user 消息的首段 text 内容。
// 兼容 string 和 []block 两种 content 格式。
func extractFirstUserText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	first := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" || msg.Get("isMeta").Bool() || msg.Get("is_meta").Bool() {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			first = content.String()
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					first = block.Get("text").String()
					return false
				}
				return true
			})
			return false
		}
		return false
	})
	return first
}

// buildBillingAttributionText 构造 system 数组的 billing attribution 文本。
//
// 形态对齐真实 Claude Code CLI：
//
//	x-anthropic-billing-header: cc_version=2.1.299.{fp}; cc_entrypoint=cli;
//
// 此处只生成已确认的基础字段。2.1.299 的条件 CCH 字段尚无最终 OAuth 发送样本，
// 不填造签名；已有 billing 文本的其他字段由版本同步逻辑保留。
//
// 此 block 不带 cache_control（与真实 CLI 一致；cache breakpoint 由后续的
// Claude Code prompt block 承担）。
func buildBillingAttributionText(body []byte, cliVersion string) (string, error) {
	if cliVersion == "" {
		return "", fmt.Errorf("cliVersion required")
	}
	fp := computeClaudeCodeFingerprint(body, cliVersion)
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli;",
		cliVersion, fp,
	), nil
}
