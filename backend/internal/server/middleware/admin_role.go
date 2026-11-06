package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// authorizeRoleRequest 按显式接口目录和请求字段检查网页登录角色权限。
func authorizeRoleRequest(c *gin.Context, users *service.UserService) bool {
	role := adminaccess.RoleFromContext(c.Request.Context())
	// 本人的合规确认是后台访问前置条件，不依赖可分配的业务权限。
	if role != nil && ((c.Request.Method == "GET" && c.FullPath() == "/api/v1/admin/compliance") ||
		(c.Request.Method == "POST" && c.FullPath() == "/api/v1/admin/compliance/accept")) {
		return true
	}
	allowed := false
	for _, permission := range adminaccess.RoleRoutePermissions(c.Request.Method, c.FullPath()) {
		if role.Allows(permission) {
			allowed = true
			break
		}
	}
	if !allowed {
		AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "当前角色未获此操作权限")
		return false
	}
	path := strings.TrimPrefix(c.FullPath(), "/api/v1/admin")
	if c.Request.Method != "GET" && strings.HasPrefix(path, "/users/:id") {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			AbortWithError(c, 400, "INVALID_USER_ID", "用户标识无效")
			return false
		}
		target, err := users.GetByID(c.Request.Context(), id)
		if err != nil {
			response.ErrorFrom(c, err)
			c.Abort()
			return false
		}
		if target.IsAdmin() || target.AdminRoleID != nil {
			AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "只有管理员可修改后台成员")
			return false
		}
	}
	needsBodyCheck := (path == "/accounts/:id" && c.Request.Method == "PUT") ||
		path == "/accounts/bulk-update" || path == "/users/batch-concurrency" || path == "/users/batch-limits" ||
		(path == "/users" && c.Request.Method == "POST") ||
		(path == "/users/:id" && c.Request.Method == "PUT") ||
		(path == "/settings" && c.Request.Method == "PUT")
	if !needsBodyCheck {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 {
		AbortWithError(c, 400, "INVALID_REQUEST", "请求无法读取或超过大小限制")
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil || body == nil {
		AbortWithError(c, 400, "INVALID_REQUEST", "请求必须是 JSON 对象")
		return false
	}
	if path == "/users/batch-concurrency" || path == "/users/batch-limits" {
		var ids []int64
		if string(body["all"]) == "true" || json.Unmarshal(body["user_ids"], &ids) != nil || len(ids) == 0 || len(ids) > 500 {
			AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "请选择最多 500 个普通用户")
			return false
		}
		if err := users.ValidateOrdinaryUserTargets(c.Request.Context(), ids); err != nil {
			response.ErrorFrom(c, err)
			c.Abort()
			return false
		}
	}
	for field, value := range body {
		permission := ""
		switch {
		case strings.HasPrefix(path, "/users"):
			switch field {
			case "admin_role_id", "role":
				if path == "/users" && ((field == "role" && string(value) == `"user"`) || (field == "admin_role_id" && string(value) == "0")) {
					continue
				}
				AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "只有管理员可分配角色")
				return false
			case "balance":
				permission = "users.balance.write"
			case "email", "password", "username", "notes", "concurrency", "rpm_limit", "status", "allowed_groups", "restrict_public_groups", "group_rates", "user_ids", "all", "mode":
				// 接口权限已区分创建、编辑及批量操作。
			default:
				AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "不支持的用户字段："+field)
				return false
			}
		case path == "/settings":
			if strings.Contains(strings.ReplaceAll(strings.ToLower(field), "_", ""), "adminapikey") {
				AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "只有管理员可管理管理员密钥")
				return false
			}
		default:
			switch field {
			case "account_ids", "filters", "confirm_mixed_channel_risk":
				continue
			case "status":
				permission = "accounts.status"
				if string(value) != `"active"` && string(value) != `"inactive"` {
					permission = "accounts.configuration"
				}
			case "name", "notes":
				permission = "accounts.update"
			case "type":
				permission = "accounts.credentials"
			case "credentials":
				if !roleAccountCredentialsAllowed(role, value) {
					AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "未获修改账号凭证或配置权限")
					return false
				}
				continue
			case "proxy_id":
				permission = "accounts.network"
			case "group_ids":
				permission = "accounts.groups"
			case "concurrency", "priority", "load_factor", "rate_multiplier", "expires_at", "auto_pause_on_expired", "schedulable":
				permission = "accounts.schedule"
			case "extra":
				if !roleAccountExtraAllowed(role, value) {
					AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "未获修改账号扩展配置权限")
					return false
				}
				continue
			case "upstream_billing_probe_enabled", "upstream_billing_rate_sync_enabled":
				permission = "accounts.test"
			default:
				AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "不支持的账号字段："+field)
				return false
			}
		}
		if permission != "" && !role.Allows(permission) {
			AbortWithError(c, 403, "ROLE_PERMISSION_DENIED", "未获修改字段权限："+field)
			return false
		}
	}
	return true
}

// AdminRoleResponses 对自定义角色的普通 JSON 响应统一剥离凭证；导出必须独立授权。
func AdminRoleResponses() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := adminaccess.RoleFromContext(c.Request.Context())
		if role == nil || isWebSocketUpgradeRequest(c) {
			c.Next()
			return
		}
		for _, permission := range adminaccess.RoleRoutePermissions(c.Request.Method, c.FullPath()) {
			if role.Allows(permission) && (strings.HasSuffix(permission, ".export")) {
				c.Next()
				return
			}
		}
		if roleAllowsNewCredentials(c, role) {
			c.Next()
			return
		}
		original := c.Writer
		buffer := response.NewBufferedWriter(original)
		c.Writer = buffer
		defer func() { c.Writer = original }()
		c.Next()
		if buffer.Streaming() {
			return
		}
		data := buffer.Body.Bytes()
		if len(data) > 0 && strings.Contains(original.Header().Get("Content-Type"), "application/json") {
			var payload any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				c.Writer = original
				response.InternalError(c, "无法处理角色响应")
				return
			}
			redactRoleSecrets(payload)
			encoded, err := json.Marshal(payload)
			if err != nil {
				c.Writer = original
				response.InternalError(c, "无法处理角色响应")
				return
			}
			data = encoded
		}
		original.Header().Del("Content-Length")
		original.Header().Set("Cache-Control", "no-store")
		original.WriteHeader(buffer.Status())
		if _, err := original.Write(data); err != nil {
			_ = c.Error(err)
		}
	}
}

// roleAllowsNewCredentials 仅向显式获写入凭证权限的会话返回本次交换产生的认证信息。
func roleAllowsNewCredentials(c *gin.Context, role *adminaccess.RoleAccess) bool {
	if c.Request.Method != "POST" || !role.Allows("accounts.credentials") {
		return false
	}
	switch strings.TrimPrefix(c.FullPath(), "/api/v1/admin") {
	case "/accounts/exchange-code", "/accounts/exchange-setup-token-code", "/accounts/cookie-auth", "/accounts/setup-token-cookie-auth",
		"/openai/exchange-code", "/openai/refresh-token", "/gemini/oauth/exchange-code",
		"/antigravity/oauth/exchange-code", "/antigravity/oauth/refresh-token",
		"/grok/oauth/exchange-code", "/grok/oauth/refresh-token", "/grok/oauth/sso-token", "/grok/oauth/password":
		return true
	default:
		return false
	}
}

// redactRoleSecrets 保留业务结构，遮蔽账号、用户 Key、代理及平台配置中的认证值。
func redactRoleSecrets(value any) {
	switch item := value.(type) {
	case map[string]any:
		_, lockSettings := item["ttl_seconds"]
		_, enabled := item["enabled"]
		_, attributeDefinition := item["validation"]
		_, options := item["options"]
		_, setting := item["value"]
		_, id := item["id"]
		_, updatedAt := item["updated_at"]
		_, backupObject := item["bucket"]
		_, etag := item["etag"]
		_, userKey := item["user_id"]
		businessKey := !userKey && ((lockSettings && enabled) || (attributeDefinition && options && id) || (setting && id && updatedAt) || (backupObject && etag))
		for key, nested := range item {
			name := strings.ToLower(key)
			if strings.HasSuffix(name, "_configured") {
				continue
			}
			sensitive := service.IsSensitiveCredentialKey(name) || (name == "key" && !businessKey) || name == "password_hash" || name == "token" || name == "apikey" || name == "credential" || name == "credentials_json" ||
				strings.Contains(name, "password") || strings.Contains(name, "secret") ||
				strings.HasSuffix(name, "_api_key") || strings.HasSuffix(name, "_access_key") || strings.HasSuffix(name, "_push_tokens") ||
				name == "authorization" || name == "cookie"
			if sensitive {
				if _, ok := nested.(bool); !ok {
					delete(item, key)
				}
				continue
			}
			redactRoleSecrets(nested)
		}
	case []any:
		for _, nested := range item {
			redactRoleSecrets(nested)
		}
	}
}

// roleAccountCredentialsAllowed 按凭证子字段限制模型、网络和认证信息的修改。
func roleAccountCredentialsAllowed(role *adminaccess.RoleAccess, raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for field, value := range fields {
		permission := "accounts.configuration"
		switch field {
		case "model_mapping", "compact_model_mapping", "model_whitelist", "model_restriction", "allowed_models":
			permission = "accounts.models.write"
		case "base_url", "api_base_urls", "proxy_fallback", "proxy_fallback_url", "header_override_enabled", "header_overrides", "protocol_rules":
			permission = "accounts.network"
		default:
			if service.IsSensitiveCredentialKey(field) || field == "client_secret" || field == "_token_version" {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return false
				}
				permission = "accounts.credentials"
			}
		}
		if !role.Allows(permission) {
			return false
		}
	}
	return true
}

// roleAccountExtraAllowed 按扩展字段语义区分网络、主动探测与常规配置权限。
func roleAccountExtraAllowed(role *adminaccess.RoleAccess, raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for field := range fields {
		permission := "accounts.configuration"
		switch field {
		case "custom_base_url_enabled", "custom_base_url":
			permission = "accounts.network"
		case "upstream_billing_probe_enabled", "upstream_billing_rate_sync_enabled":
			permission = "accounts.test"
		}
		if !role.Allows(permission) {
			return false
		}
	}
	return true
}
