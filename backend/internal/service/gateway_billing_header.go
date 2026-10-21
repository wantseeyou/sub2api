package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ccVersionInBillingRe matches the semver part of cc_version (X.Y.Z).
var ccVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

var ccVersionWithFingerprintInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+\.[0-9a-fA-F]{3}\b`)

var ccEntrypointInBillingRe = regexp.MustCompile(`cc_entrypoint=[^;\s]+`)

const claudeBillingSourceTextKey = "claude_billing_source_text"

// captureClaudeBillingSource 在任何网关改写前保存取样文本，同一请求的重试与账号切换复用该值。
func captureClaudeBillingSource(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	if _, exists := c.Get(claudeBillingSourceTextKey); !exists {
		c.Set(claudeBillingSourceTextKey, extractFirstUserText(body))
	}
}

// effectiveBillingUserAgent 让所有 OAuth 请求的 billing 与固定出站 UA 同源。
func effectiveBillingUserAgent(userAgent, tokenType string) string {
	if tokenType != "oauth" {
		return ""
	}
	return userAgent
}

// syncBillingHeaderVersion 同步 billing 的版本、后缀和 cli 入口，保留其他条件字段。
func syncBillingHeaderVersion(body []byte, userAgent string, c *gin.Context) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	firstText := extractFirstUserText(body)
	if c != nil {
		if saved, exists := c.Get(claudeBillingSourceTextKey); exists {
			if text, ok := saved.(string); ok {
				firstText = text
			}
		}
	}
	fingerprint := computeClaudeCodeFingerprintFromText(firstText, version)
	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			fingerprintedReplacement := replacement + "." + fingerprint
			newText := ccVersionWithFingerprintInBillingRe.ReplaceAllString(text.String(), fingerprintedReplacement)
			newText = ccVersionInBillingRe.ReplaceAllString(newText, replacement)
			newText = ccEntrypointInBillingRe.ReplaceAllString(newText, "cc_entrypoint=cli")
			if newText != text.String() {
				if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), newText); err == nil {
					body = updated
				}
			}
		}
		idx++
		return true
	})

	return body
}
