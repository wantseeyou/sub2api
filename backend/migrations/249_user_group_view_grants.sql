-- 查看授权独立于模型调用资格；回滚应用时可保留此表，无需修改历史用量。
CREATE TABLE IF NOT EXISTS user_group_view_grants (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_user_group_view_grants_group_id
    ON user_group_view_grants (group_id);
