# 外部账号同步设计

## 背景

`pty` 分支已经新增 token 导出接口：

- `GET /api/v1/accounts/token-export`
- 密码可以通过 `X-Token-Export-Password` 请求头传入，也可以通过 query 参数 `password` 传入。
- 响应包含 `items`，每条数据包含 `id`、`name`、`platform`、`type`、`status`、`access_token`、`refresh_token`、`expires_at`、`credentials`、`extra` 和 `updated_at`。

`my` 分支需要从另一个 Sub2API 实例消费这个接口，并把本地账号保持同步。源地址在后台系统设置的通用配置里填写完整 URL；如果需要密码，可以直接把 `password` query 写在 URL 里。

## 目标

- 新增一个独立后台服务，每 10 秒轮询一次配置的 token export URL。
- 根据导出的 `items` 对本地账号做新增或更新。
- 使用 `credentials.email` 匹配账号，不使用源实例的 `id`。
- `credentials.email` 为空的导出账号直接跳过。
- 给通过同步创建或更新的账号打上外部托管标记。
- 外部托管账号跳过本地默认 OAuth token 刷新流程。
- 当请求链路发现外部托管账号 token 失效时，触发一次立即同步。

## 非目标

- 不修改 `pty` 分支已有的源导出接口。
- 除非实现时证明 JSON 查询性能或可靠性不够，否则不新增数据库 migration。
- 不同步缺少 `credentials.email` 的账号。
- 不把导出的 `id` 当作本地账号身份。源实例和目标实例是两套数据库，数字 ID 不能跨实例复用。
- 本次不做多源同步系统。

## 配置

在后台通用配置里新增一个设置项：

- `external_account_sync_url`：token export 接口的完整 URL。

示例：

```text
https://source.example.com/api/v1/accounts/token-export?password=123456
```

空值表示禁用同步服务。这个 URL 可能包含敏感密码，因此日志和 UI 展示应避免在运维日志里暴露 query secret。

## 账号匹配规则

每条导出账号按以下流程处理：

1. 从 `item.credentials.email` 读取 `email`。
2. 对 `email` 做 trim。
3. 如果 `email` 为空，跳过该账号。
4. 查找本地账号，匹配条件为：
   - `platform == item.platform`
   - `type == item.type`
   - `credentials.email == email`
5. 如果只匹配到一个本地账号，更新该账号。
6. 如果没有匹配到本地账号，新增账号。
7. 如果匹配到多个本地账号，记录 warning 并跳过该账号，避免误更新。

导出的 `id` 只作为来源元数据保存，不作为本地账号主键或匹配键。

## 账号筛选实现

账号筛选建议封装在 repository 层，新增类似下面的方法：

```go
ListByPlatformTypeCredentialEmail(ctx context.Context, platform, accountType, email string) ([]Account, error)
```

实现要点：

- service 层只传入 trim 后的 `email`。
- repository 层同时过滤 `platform`、`type` 和 `credentials.email`。
- JSON 条件优先使用项目已有的 `sqljson.ValueEQ` 写法，例如按 `sqljson.Path("email")` 匹配 `credentials` 字段，避免手写 SQL 导致数据库方言差异。
- 该方法允许返回 0、1 或多条结果；多条结果由同步服务记录 warning 并跳过。
- 本次不新增唯一索引，因为历史数据中可能已经存在重复账号，直接加唯一约束会带来 migration 风险。

## 托管账号标记

通过该同步创建或更新的账号，在 `extra` 中写入标记：

```json
{
  "external_token_export_managed": true,
  "external_token_export_email": "user@example.com",
  "external_token_export_source_id": 123,
  "external_token_export_updated_at": "2026-06-26T10:00:00Z"
}
```

本地后台 token 刷新逻辑通过这个标记识别外部托管账号，并跳过默认刷新。

## 新增和更新行为

如果本地已有账号，更新：

- `name`
- `platform`
- `type`
- `status`
- `credentials`
- `extra`，并合并外部托管标记

如果本地没有账号，新增：

- `name`
- `platform`
- `type`
- `status`
- `credentials`
- `extra`，包含外部托管标记

新账号默认值优先沿用现有 repository/service 的默认行为。如果创建路径需要显式传默认值，使用保守的运行默认值：

- 导出 `status` 为 active 时，`schedulable = true`
- `concurrency` 优先使用现有账号创建默认值，否则使用手动创建账号相同的默认值
- `priority` 优先使用现有账号创建默认值，否则使用手动创建账号相同的默认值

## 轮询服务

新增独立 `ExternalAccountSyncService`，提供：

- `Start()`
- `Stop()`
- `SyncOnce(ctx, reason string) error`
- `TriggerNow(reason string)`

行为：

- 服务启动后立即同步一次。
- 之后每 10 秒同步一次。
- 如果配置为空，本轮不做同步，并等待下一轮或后续设置更新。
- HTTP 请求需要设置 timeout，避免上游卡死导致服务挂住。
- 使用 singleflight 或非阻塞锁，避免定时同步和立即同步并发执行。
- 日志记录统计信息：拉取数量、空 email 跳过数量、新增数量、更新数量、重复命中跳过数量、失败数量。
- 记录错误日志时对配置 URL 做脱敏。

## Token 失效触发

当请求链路检测到外部托管账号 token 失效时，调用：

```go
externalAccountSyncService.TriggerNow("token_invalid")
```

触发逻辑是 best effort：

- 不能长时间阻塞网关请求。
- 不能创建不受控 goroutine。
- 如果已有同步正在执行，重复触发需要合并。

## 默认 Token 刷新排除

现有 `TokenRefreshService` 需要跳过满足以下条件的账号：

```go
account.Extra["external_token_export_managed"] == true
```

这样可以避免两个实例同时刷新同一个上游 token。源实例负责刷新 token；目标实例只导入最新凭证。

## 错误处理

- 配置 URL 非法：记录 warning，跳过本轮。
- 上游返回非 2xx：记录 warning，跳过本轮。
- JSON 解析失败：记录 warning，跳过本轮。
- `items` 为空：视为同步成功，但没有变更。
- 缺少或空白 `credentials.email`：跳过该账号。
- 本地重复命中：跳过该账号，并记录命中的 account IDs。
- 单个账号创建或更新失败：记录该账号 email，继续处理后续账号。

## 影响范围

预计实现会涉及：

- 后端 settings model 和 handler，新增 `external_account_sync_url`。
- 后台系统设置通用配置 UI，新增 URL 输入项。
- 新增后端外部账号同步服务。
- Account repository 增加按 `platform`、`type`、`credentials.email` 查询账号的能力。
- Token refresh 增加外部托管账号跳过逻辑。
- 网关 token-invalid 处理路径触发立即同步。
- 服务 wiring 和生命周期 start/stop。

风险等级：中等。该改动涉及认证凭证和调度可见的账号状态，但可以通过默认空配置禁用，并通过 JSON `extra` 标记隔离行为。

## 验证策略

- 单元测试同步响应解析和空 email 跳过行为。
- 单元测试账号匹配和重复命中跳过行为。
- 单元测试外部托管账号会被后台 token refresh 跳过。
- 单元测试已有同步运行时，立即触发会被合并。
- 运行 settings、account repository/service、token refresh 相关的聚焦后端测试。
- 如果环境依赖允许，运行 `go test ./...`。
