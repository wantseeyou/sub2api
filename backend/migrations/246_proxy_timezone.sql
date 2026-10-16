-- 新增独立时区配置与成功检测快照，已有代理无需回填。
-- 回滚应用时可保留这两个字段，避免丢失管理员保存的数据。
ALTER TABLE proxies
    ADD COLUMN IF NOT EXISTS timezone VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS timezone_detection JSONB;
