# 实施与验证记录

日期：2026-10-19。基于 `6039dca84` 实施，范围为 proposal 的八项目标。未提交、部署或调用真实上游。

## 使用与兼容性

账号创建、编辑的 Codex 高级设置增加两个开关：

| 配置 | 默认值 | 行为 |
| --- | --- | --- |
| `openai_http_to_ws_enabled` | `false` | HTTP 入站可选择 WS 上游，客户端仍按原请求接收 SSE 或 JSON；账号能力、强制 HTTP、compact、插件限制继续优先，后台请求保留 HTTP。 |
| `codex_thread_isolation_enabled` | `false` | 按调用方和原始线程派生身份，启用后须新建会话；旧 response 链与状态不会迁移。 |

Codex HTTP 压缩仅作用于 `https://chatgpt.com/backend-api/codex/responses` 的可重读 JSON POST 请求副本。线程、回合和窗口保真修复直接生效。WS 请求发送后执行情况不明时返回错误，不透明重放；明确拒绝后的安全纠正仍有次数限制。

新隔离模式的续链来源证明保存在本实例的 TTL 缓存中，最多 8192 项。实例重启、切换或缓存淘汰后，无法验证的续链需要新建会话；多实例部署启用前应评估会话路由。关闭两个新开关可恢复原有 HTTP 入站及线程派生方式，已开始的会话不跨身份规则迁移。

管理员在账号操作菜单打开“模型一致性检测”。每次最多选择 8 个模型，每模型最多 3 份独立样本；全局最多 4 个运行、同账号串行，沿用账号并发限制。单份样本超时 45 秒，整批截止 10 分钟。未覆盖模型直接显示无法判定，不向上游采样。

接口均位于管理员鉴权路由下：

- `GET /api/v1/admin/accounts/:id/model-consistency`：读取最近结果与支持范围。
- `POST /api/v1/admin/accounts/:id/model-consistency`，请求体 `{"models":["..."]}`：启动检测。
- `DELETE /api/v1/admin/accounts/:id/model-consistency/:run_id`：取消指定批次。

只支持可安全复用现有有效凭据的 OpenAI OAuth/SetupToken 账号，包含符合条件的 PAT 和影子账号；排除 API Key 和 Agent Identity。检测使用独立线程、回合、缓存键与 WS 连接，不改变业务调度或用户账单，不注入检测取得的状态。真实检测会消耗上游额度，取消不代表上游未计费。

## 数据与并发影响

最近结果保存在服务端托管的 `accounts.extra.model_consistency_results`，不建表、不保留历史。普通创建、编辑、批量、导入不能写入快照，复制账号不继承快照，DTO 只输出白名单字段。

Redis 租约限制跨实例运行并传递取消。每个模型完成后即通过 `run_id`、配置摘要、运行状态、截止时间及账号/代理版本条件保存，后续模型取消或超时不会丢失已完成结果。每批最多 8 个模型分别保存，不逐样本写入；保存前重新读取配置，正常使用时间更新造成 CAS 冲突时最多重试一次。检测快照不触发调度状态更新。

## 已执行验证

- 主转发边界回归通过：普通/透传 SSE 与 JSON、心跳交付响应头后断流、单一失败终态、WS 发送后 EOF/`response.created` 后断连、续链锚点保留及密文纠正。
- 身份与原生 WS 回归通过：线程/回合来源、执行作用域、连接复用、HTTP bridge、续链失效与预检边界；handler 回归通过（4.475s）。最终续链边界 `-race` 通过（18.927s），覆盖工具推断锚点、强制 HTTP bridge 的外部链、后续回合 429，以及 native/bridge × 增量或三种完整 assistant 历史表示的 8 个场景；恢复输入无重复且文本保真。
- 后端相关回归通过：`service`、`repository`、`handler`、`handler/admin`、`handler/dto`、`cmd/server`；`server/routes` 编译通过，所选模式无测试用例。执行命令：

  ```sh
  go test ./internal/service ./internal/repository ./internal/handler ./internal/handler/admin ./internal/handler/dto ./internal/server/routes ./cmd/server -run 'Test(OpenAI|Codex|ClassifyOpenAI|ResolveOpenAI|Handle.*OpenAI|Build.*OpenAI|Account|Admin|ProvideCleanup|Managed|PluginManagerRouting)' -count=1 -timeout=5m
  ```

- 转发与压缩临时验证使用 `-race` 通过：普通/透传 SSE 与 JSON、默认关闭、强制 HTTP/compact/插件限制、后台请求、部分发送错误、发送后断连、心跳头提交与失败终态、压缩等价性及原请求可重读。
- 诊断专项 `-race` 通过：四种指纹模式的独立身份、PAT/影子凭据、严格成功终态、取消、WS 不重放。隔离 PostgreSQL 原子保存、并发编辑及母凭据变化验证通过；模拟 Redis 的共享租约与取消通知通过。两次中性更新时间变化后有界重试保存的临时回归通过。
- Wire 生成及清理入口验证通过，未修改 `go.mod`、`go.sum`。
- 前端 7 个既有测试文件、157 项通过；18 项独立临时验证通过；11 个改动文件 ESLint 通过。
- `pnpm build` 通过，包括 i18n 键检查、`vue-tsc -b` 和 Vite。构建仍提示 Browserslist 数据陈旧、混合静态/动态导入及部分分包超过 500 kB。

- 初次统一审查发现的诊断终态保存冲突、续链完整性判断、bridge 外部锚点及 assistant 历史恢复问题均已修复。后续复查的三个问题及修复见下方记录。
- 最终 `CGO_ENABLED=0 go build -o /tmp/sub2api-codex-validation-server ./cmd/server` 通过。53 个改动 Go 文件 `gofmt` 检查及 `git diff --check HEAD` 通过。
- 最终后端静态检查通过（0 issues）：使用与 CI 对齐的 golangci-lint v2.14.0，并以 Go 1.27.2 构建工具，执行 `golangci-lint run --timeout=8m --new-from-rev=HEAD ./internal/service/... ./internal/repository/... ./internal/handler/... ./internal/server/routes/... ./internal/pkg/modeltrace/... ./cmd/server/...`。
- GitNexus 最终影响分析标记为 CRITICAL：索引识别 48 个既有文件、127 个符号及 17 个流程，集中于转发、WS 透传和账号结果保护；新增未索引文件另由代码审查、测试及构建覆盖。

临时测试文件、隔离 PostgreSQL 实例及其数据已清理；未新增长期测试文件。

## 复查修复（2026-10-19）

- 已修复整批超时或取消丢失已完成模型结果：每模型完成即条件保存，保留样本数、判定和用量；服务内部取消仍将剩余模型结束为取消状态，不继续采样。
- 已修复原生 WS 重连沿用首回合元数据：首帧及后续重连均从对应帧改写前的元数据构造握手，避免重复账号命名空间变换；后续帧缺少回合元数据时不复用首回合。
- 已修复 HTTP→WS 透传的大整数精度丢失：复用 `decodeOpenAIJSONUseNumber`，工具 Schema 的 `const:9007199254740993` 在 HTTP 与 WS 出站均保持原值。

本轮临时验证通过（1.732s），同组 `-race` 通过（3.251s）：覆盖上述三个复现场景、有/无账号命名空间、外部及内部取消、CAS 重试和配置变化拒写。使用本地模拟上游及内存条件仓储，未调用真实上游。

```sh
go test ./internal/service -run '^TestReview(Spec|Standards)' -count=1 -v -timeout=2m
go test -race ./internal/service -run '^TestReview(Spec|Standards)' -count=1 -timeout=2m
```

相关既有回归通过（5.719s）：

```sh
go test ./internal/service -run 'Test(OpenAIGatewayService_ProxyResponsesWebSocketFromClient|OpenAIPassthrough|ApplyCodexFingerprint|ResolveCodexFingerprint|BuildUpstreamRequestOpenAIPassthrough)' -count=1 -timeout=5m
```

本轮临时用例验证后已删除，不作为长期测试保留。三处 Go 文件格式检查及 `git diff --check HEAD` 通过；`.dev/start.sh --restart-backend` 完成后端构建和本地重启，前端 3000、后端 8081 健康检查通过。

发布 `0.2.172` 前，包含前端资源的 `CGO_ENABLED=0 go build -tags=embed` 通过，二进制版本输出确认为 `0.2.172`。远端完整静态检查发现身份解析统一后遗留的未使用函数 `codexTurnMetadataThreadID`，已删除；线程和执行作用域相关测试通过（1.728s）。随后以 Go 1.27.2 运行 golangci-lint v2.14.0 完整检查 `./...`，结果为 `0 issues`，未使用差异过滤。

## 验证限制

未使用真实账号、代理或插件运行线上探测，未测得线上首输出耗时、失败率或压缩收益。上线后可观察出站字节数、WS 连接建立次数、首输出时间与失败阶段；这些改动不保证减少输入 token 或提高缓存命中率。

模型判定仅是三份完整有效样本与固定参考指纹的经验关系，不是身份认证或能力评分。参考采集与当前 Codex 协议转换、模型版本未独立校准，分数不是准确率。固定来源、profile 与限制见 `backend/internal/pkg/modeltrace/SOURCE.md`。
