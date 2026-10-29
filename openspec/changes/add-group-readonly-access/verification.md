# 分组只读查看验收记录

日期：2026-10-29。源码实现、本地验收与代码审查已完成；目标发布版本 `0.2.191`，未部署业务环境、未操作业务数据库。

## 实现入口与约束

- 管理员：用户管理 → 用户操作 → 分组查看授权。GET/PUT `/api/v1/admin/users/:id/group-view-grants` 仅接受已登录管理员的 JWT，不向传统或受限 Admin API Key 开放。
- 查看者：有有效授权时显示“分组查看”菜单，页面路径 `/group-view`。普通用户身份和个人 API Key、模型调用资格保持原有规则。
- 读取接口：`/api/v1/group-view/{access,accounts,people,usage,summary,options}`。`access` 无授权时返回空数组，数据接口无有效授权时返回 403。
- 省略 `group_ids` 使用全部有效授权；显式未授权分组返回 403。每个请求从数据库读取授权，不使用权限缓存。
- 分页默认 20 条、最多 100 条；页码最多 1,000,000。人员、明细、汇总和候选要求 RFC3339 时间，使用左闭右开范围，最长 366 天。
- 账号按当前组绑定筛选；历史统计统一使用 `usage_logs.group_id`。共享上游额度只读取已有快照；缺少快照显示暂无数据，不触发探测或凭据刷新。

后端集中于 `backend/internal/groupview/`，新增迁移 `backend/migrations/249_user_group_view_grants.sql`。前端集中于 `GroupView.vue`、`GroupViewResourceFilter.vue` 和 `UserGroupViewGrantsModal.vue`，复用现有表格、分页与趋势图。

## 已执行检查

| 检查 | 结果 |
| --- | --- |
| 后端 `go test ./...` | 全部通过 |
| 后端 `go vet ./...` | 通过 |
| 后端 `golangci-lint run ./internal/groupview/...` | 0 issues |
| 后端 `go build -o /tmp/sub2api-group-view-server ./cmd/server` | 最终实现构建通过 |
| 发布前 `go build -tags embed` 及 `-version` | 内嵌最终前端资源构建通过，实际输出版本 `0.2.191` |
| 后端 `go test ./internal/groupview ./internal/server/routes ./migrations` | 最终相关回归通过；临时用例删除后 `groupview` 无保留测试 |
| 修改 Go 文件 `gofmt -l`、`git diff --check` | 无格式问题 |
| Wire 生成 | 成功；工具依赖使用临时 modfile，项目依赖文件未改变 |
| 前端 `pnpm typecheck`、修改文件 ESLint | 通过；刷新修复后的 `GroupView.vue` 再次通过 ESLint |
| 前端相关既有测试 | 47 项通过：AppSidebar、UsersView、auth、localeKeyCompleteness、localesMessageCompile |
| 前端临时组件交互测试 | 8 项全部通过，随后删除 |
| 发布前分页回归 | 3 项临时用例、Pagination 4 项和 tablePreferences 6 项既有用例通过；临时用例随后删除 |
| 前端 `pnpm build` | 最终实现通过，包含文案检查、`vue-tsc -b` 和 Vite 生产构建 |
| 隔离 PostgreSQL 迁移 | 全部 301 个迁移首次及重复应用均通过 |
| 临时后端验收 | `-race` 通过；另有 9 组额度快照 SQL 场景通过 |

前端构建仍提示 Browserslist 数据过期、Node 废弃 API、混合静态/动态导入及已有大体积 chunk；构建成功，本次未调整依赖或构建配置。

## 行为覆盖

- G1/G2 与 G3 的账号、人员、明细、汇总、趋势及候选隔离；篡改组或资源条件不能扩大范围。
- 同一账号跨多个授权组只列一次，标签不包含未选组；共享账号的其他组用量不进入统计。响应不包含完整凭据、上游成本、倍率、余额、内部备注、IP 或 User-Agent。
- Key 删除、Key 调组、账号离组后历史仍按日志归属；人员仅来自所选时段的授权组记录。软删除组不再形成有效授权。
- 列表和汇总使用一致筛选，验证稳定排序、双向排序及分页。账号可调度状态复用既有 `IsSchedulable()` 的额度与过期规则。
- 验证匿名、无授权、撤权、非法输入及直接调用管理授权接口的限制；同一用户并发替换授权保持完整集合，非法分组替换失败不丢失原授权。
- 组件验收覆盖筛选联动、立即清除旧数据、丢弃过期响应、撤权后清空页面及授权弹窗加载失败时禁止保存。
- 最终审查发现并修复：选中分组被撤销后，刷新清除失效选项并回首页；剩余选择为空时读取全部剩余授权，避免持续携带旧组导致 403。
- 发布前按用户要求使用 `gpt-6-astra` 子代理独立审查，发现并修复 1 个 P2：全局分页配置可包含 `1000`，而新接口最多接受 `100`。本页使用上限内的独立选择器并校验值，保留共享 Pagination 的翻页功能。配置 `[20,50,1000]`、全部选项超限、正常选项及三个页签均已验证；同一子代理复核通过，未发现 P0/P1 或新的具体问题。

## 查询计划与验证限制

在隔离 PostgreSQL 中生成 100,000 条模拟使用记录，目标组和时间范围命中现有 `idx_usage_logs_group_created_at_not_null`，范围索引扫描 4 条；代表性读取与聚合执行约 0.032–0.090 ms，无需新增使用记录索引。该结果仅说明本地测试数据的查询计划，不代表生产规模或延迟。

GitNexus 初始索引落后 65 个提交。实施前已对既有接线点执行影响分析，并报告 HIGH/CRITICAL 风险；索引更新失败，后续工具返回 `Transport closed`。发布前再次调用 `detect_changes` 仍连接失败，本地 CLI 的同等检查也因现有索引 WAL 恢复问题退出。因此补充了当前源码调用范围核对与独立代码审查，不能将旧索引视为完整影响证明；未删除或重置索引数据。

页面交互使用组件测试验证，未进行浏览器与实际业务库的端到端验收。临时测试与脚本已删除，临时 PostgreSQL 已停止并删除，临时 Wire 依赖、日志和后端二进制已清理。

## 上线与回滚

随正常发布流程启动新版服务时，由既有迁移机制执行 `249_user_group_view_grants.sql`；现有用户默认无查看授权，无需回填。上线后由管理员显式配置分组。

回滚时恢复旧版应用并保留新授权表即可，无需改写历史用量、角色或计费数据。同组数据对该组所有查看者可见，需要公司隔离时应使用不同分组；已展示或保存的数据无法通过撤权收回。
