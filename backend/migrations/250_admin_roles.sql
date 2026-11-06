-- 增量迁移：保留 admin/user 与模型调用权限；回滚应用可保留新增表和字段。
CREATE TABLE IF NOT EXISTS admin_roles (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(64) NOT NULL UNIQUE,
    description VARCHAR NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(permissions) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS admin_role_id BIGINT REFERENCES admin_roles(id) ON DELETE RESTRICT;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_admin_role_identity' AND conrelid = 'users'::regclass) THEN
        ALTER TABLE users ADD CONSTRAINT users_admin_role_identity CHECK (admin_role_id IS NULL OR role = 'user');
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_users_admin_role_id ON users (admin_role_id);

-- 助理只预置常规查看与账号重新授权、启停；后续新增权限不会自动授予。
INSERT INTO admin_roles (name, description, permissions)
VALUES ('助理', '查看常规后台业务数据，重新授权及启用、停用上游账号', '["prompt_audit.read", "compliance.read", "risk_control.read", "ops.read", "dashboard.read", "users.read", "api_keys.read", "usage.read", "users.balance.read", "users.quotas.read", "groups.read", "accounts.read", "announcements.read", "proxies.read", "redeem_codes.read", "promo.read", "settings.read", "data_management.read", "backups.read", "system.read", "subscriptions.read", "user_attributes.read", "scheduled_tests.read", "error_passthrough.read", "tls_fingerprints.read", "plugins.read", "channels.read", "channel_monitors.read", "affiliates.read", "channel_monitor_v2.read", "payment.read", "payment.config.read", "payment.plans.read", "payment.providers.read", "accounts.reauthorize", "accounts.status"]'::jsonb)
ON CONFLICT (name) DO NOTHING;
