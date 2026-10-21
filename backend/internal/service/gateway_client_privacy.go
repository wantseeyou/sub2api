package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicfp"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// prepareClaudeOAuthClientBody 在最终出口统一处理客户端身份与自动环境信息。
func (s *GatewayService) prepareClaudeOAuthClientBody(ctx context.Context, account *Account, body []byte, headers http.Header) ([]byte, *Fingerprint, error) {
	if !account.IsAnthropicOAuthOrSetupToken() {
		return body, nil, nil
	}
	var fp *Fingerprint
	var err error
	if s.identityService != nil {
		fp, err = s.identityService.GetOrCreateFingerprint(ctx, account.ID, headers)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare OAuth identity: %w", err)
		}
		body, err = s.identityService.RewriteUserIDWithMasking(ctx, body, account, account.GetExtraString("account_uuid"), fp.ClientID, fp.UserAgent)
	} else if gjson.GetBytes(body, "metadata").Exists() {
		// 未装配身份服务时省略可选 metadata，不能透传客户端的原始身份。
		body, err = sjson.DeleteBytes(body, "metadata")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("prepare OAuth metadata: %w", err)
	}
	body, err = anthropicfp.NormalizeClientEnvironment(body, time.Now().UTC())
	if err != nil {
		return nil, nil, fmt.Errorf("prepare OAuth environment: %w", err)
	}
	return body, fp, nil
}

// syncClaudeOAuthSessionHeader 仅允许会话头引用已经替换后的 metadata 会话标识。
func syncClaudeOAuthSessionHeader(req *http.Request, body []byte) {
	hadSessionHeader := false
	for name := range req.Header {
		if strings.EqualFold(name, "X-Claude-Code-Session-Id") {
			hadSessionHeader = true
			break
		}
	}
	deleteHeaderAllForms(req.Header, "X-Claude-Code-Session-Id")
	if !hadSessionHeader {
		return
	}
	if parsed := ParseMetadataUserID(gjson.GetBytes(body, "metadata.user_id").String()); parsed != nil {
		setHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
	}
}
