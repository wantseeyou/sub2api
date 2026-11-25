//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRoleGroupSyncPermissions 验证自动同步名称同时需要编辑和探测权限，预览仅需探测权限。
func TestRoleGroupSyncPermissions(t *testing.T) {
	for _, tc := range []struct {
		name        string   // 权限组合名称。
		permissions []string // 本次请求的授权集合。
		allowed     bool     // 是否允许修改同步设置。
	}{
		{"update only", []string{"accounts.update"}, false},
		{"test only", []string{"accounts.test"}, false},
		{"both", []string{"accounts.update", "accounts.test"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role := &adminaccess.RoleAccess{Permissions: tc.permissions}
			router := gin.New()
			router.PUT("/api/v1/admin/accounts/:id", func(c *gin.Context) {
				c.Request = c.Request.WithContext(adminaccess.WithRole(c.Request.Context(), role))
				if authorizeRoleRequest(c, nil) {
					c.Status(http.StatusNoContent)
				}
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/1", strings.NewReader("{\"upstream_billing_group_sync_enabled\":true}"))
			router.ServeHTTP(recorder, request)
			if tc.allowed {
				require.Equal(t, http.StatusNoContent, recorder.Code)
			} else {
				require.Equal(t, http.StatusForbidden, recorder.Code)
			}
		})
	}
	require.Equal(t, []string{"accounts.test"}, adminaccess.RoleRoutePermissions(http.MethodPost, "/api/v1/admin/accounts/upstream-billing-probe/group-preview"))
}
