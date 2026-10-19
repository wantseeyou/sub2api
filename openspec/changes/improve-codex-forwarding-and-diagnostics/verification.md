# 实施与验证记录

日期：2026-10-19。初版基于 `6039dca84` 实施，八项目标已随 0.2.172 发布；本文后部记录尚未发布的线上反馈修复与新增量优化。

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

## 0.2.172 线上反馈修复（2026-10-19，未发布）

- 批量编辑增加“HTTP 请求使用 WS 上游”，仅支持选中的账号均为 OpenAI OAuth/SetupToken；勾选才修改，可与 `ctx_pool` 同批保存，单独修改 WS mode 不覆盖此开关。
- 模型检测保留失败请求的真实 HTTP/WS 握手状态，按鉴权、权限、限流、模型、请求参数、服务错误分类。仅有 200/101 等状态但握手失败时，记录 `websocket_handshake_rejected`。错误正文最多读取 8 KiB，仅用于分类，不持久化或展示。
- SSE/WS 流中的 `error`、`response.failed` 也按已知错误码分类，包括 `server_is_overloaded`；未知错误继续显示 `response_failed`，不将事件错误推测成 HTTP 状态码。
- WS 执行结果不明时，管理员事件保留阶段、原因和关闭码；客户端错误及不自动重放的行为保持不变。仅真实握手状态进入上游 HTTP 状态字段，不再将合成的 502/400/426 当成真实上游状态。
- 无数据库迁移，继续使用 `accounts.extra` 和既有错误事件字段；旧检测结果兼容，但不会补出此前未保存的信息。

验证：后端诊断、WS、转发及批量更新相关回归通过（39.455s）；前端既有批量测试 54 项及临时边界验证 5 项通过；五个改动前端文件 ESLint、`pnpm build`、完整 service 静态检查通过（0 issues）。临时用例覆盖错误正文读取上限、读取失败、正常 SSE、WS 握手、真实与合成状态区分、独占连接不重放、三个 Responses 路由别名以及批量开关关闭与重置。

独立审查发现的合成状态混入上游状态、非错误 HTTP 状态握手失败丢失两项边界问题，均先以临时用例复现，再修复验证。临时测试已删除；本地后端重建并重启，8081 健康检查与 3000 账号页面均返回 200。GitNexus 索引更新失败，最终 `detect_changes` 返回 `Transport closed`，本轮图谱检查受限，调用链与影响范围已用源码和回归补充核对。

线上只读核查：账号 29 的 `server_is_overloaded` 是上游过载；账号 49 的 `downstream_write_error` 是下游写入失败。账号 45 的 15:24:26（北京时间）请求仅保存通用 502，运行日志级别为 `error`，缺少对应阶段日志，无法追溯具体原因。该账号随后已有多条 WSv2 用量记录，说明 WS 通路可用，不能据此证明那次故障的根因或宣称其已修复。

## 连接内增量优化（未发布）

普通 OpenAI OAuth/SetupToken 的 HTTP→WS（含 HTTP 透传）和原生 `ctx_pool` 共用同一摘要比较逻辑。仅在调用方、分组、账号、原始线程及窗口一致，参数相同，当前完整历史以前次输入与完整输出为前缀时裁剪。显式 `previous_response_id`（包括 null/空字符串）、预热、API Key 账号及身份不足的请求不自动裁剪。模型、推理参数、工具或历史变化继续全量发送；原始输入保留给失败恢复。

每连接只保留最后成功响应的 ID、参数摘要及至多 2048 个 SHA-256 摘要（64 KiB，不含固定字段），不持久化正文。比较成本与本次输入字节数线性相关；不新增 I/O、goroutine 或跨连接共享锁。每次写请求先清空旧快照，完整 `response.completed/status=completed` 后才重新登记；换连接天然失效。

只有自动锚点被首个事件明确以 `previous_response_not_found` 拒绝、且原有下游响应提交边界允许重试时，才丢弃坏连接并恢复原完整请求一次。执行不明或已收到其他事件时不自动重放。debug 日志 `incremental_input` / `ingress_ws_incremental_input` 中的 `removed_items`、`payload_bytes` 可确认实际裁剪。

验证：相关既有转发、WS、模型检测及批量更新回归通过（38.277s）。临时验证覆盖三轮续接、HTTP/HTTP 透传/原生 WS 的 12 个正常及失败场景、身份和窗口隔离、参数与历史变化、工具结果、大整数、显式空锚点、未完成输出与摘要容量上限；专项竞态检查通过（3.970s）。

最终独立审查发现 HTTP 自动锚点失效后，一次性恢复约束仅依赖最近错误原因，中间的获取连接失败可能使裁剪重新启用。临时验证已复现，修为整个请求持续保留恢复标记；HTTP 常规与透传共用，原生路径沿用回合标记。修复后增量、相关重试和连接池竞态回归通过（13.466s）。

```sh
go test ./internal/service -run 'Test(WSIncremental|ModelConsistency|OpenAIWS|OpenAIGatewayService_.*(Forward|ProxyResponsesWebSocket)|OpenAIPassthrough|Bulk.*Account)' -count=1 -timeout=6m
go test -race ./internal/service -run 'Test(WSIncremental|ModelConsistencyEventReasonTemp|OpenAIWS.*(Fallback|Retry|Pool))' -count=1 -timeout=4m
```

最终 service 静态检查（Go 1.27.2 / golangci-lint v2.14.0）通过，`0 issues`；后端构建并重启成功，8081 健康检查与 3000 账号页面可用。临时测试验证后已删除，不作为长期测试提交。前端沿用本轮已通过的 59 项测试、ESLint 与构建结果，其后未再改动前端。GitNexus 仍返回 `Transport closed`，本轮调用图检查受限，已用源码、回归和独立审查核对影响范围。本轮未提交、推送或发布。

## 大请求、连接寿命和网页搜索兼容（2026-10-19，未发布）

线上只读核查确认三类独立问题：`message_too_big`（对端关闭码 1009）、`websocket_connection_limit_reached`（60 分钟）和不支持图片网页搜索。旧日志未保存报文字节数，不能据此推算上游实际大小上限。

- HTTP 普通和透传路径复用 `gateway.openai_ws.http_bridge_enabled` / `http_bridge_threshold_bytes`，默认开启、15 MiB。先检查原始大小，再按实际 JSON 编码检查；编码后超限时，拨号和发送前从原始请求重新执行 HTTP 转换，保留 namespace、身份及工具语义。显式续链超限返回 413，不删除锚点或重放。原生 ctx_pool 后续帧也检查实际编码大小，超限要求客户端提供更小请求或完整 HTTP 请求。
- 池连接的轮换窗口提前到 55 分钟，后台不关闭正在使用的连接。租借时淘汰到龄连接，原生 ctx_pool 在回合边界用完整历史恢复；工具结果必须有配对调用。`store=false` 的 HTTP 续链连接失效或到龄时返回明确 400，要求完整上下文；可持久化的 API Key 续链仍可在新连接继续。单个回合跨过 60 分钟硬限制仍可能失败，执行不明时继续禁止自动重放。
- 普通 OAuth/SetupToken 的 `web_search` 混合文字/图片声明保留文字，image-only 明确拒绝，覆盖 HTTP、原生 ctx_pool、原生 WS 透传及 Lite `additional_tools`。现有 OAuth 模型清单同步将 `text_and_image` 调整为 `text`，客户端 ETag 随正文变化，上游缓存重验证仍使用原 ETag；不扩大既有清单接口对 SetupToken 的支持范围。API Key、Agent Identity、图片输入和图片生成不受此规则影响。
- 无数据库迁移、不新增配置项或后台任务；额外检查随请求字节数线性增长，HTTP 编码结果供后续增量比较和发送复用，原生小帧不额外编码。

验证使用本地模拟上游：相关既有回归及临时验证通过（33.199s）；连接池、续链、模型清单与本轮边界的竞态检查通过（14.680s）。独立审查发现原生 WS 透传首帧和后续帧丢失 image-only 的明确错误原因，已统一为 1008 并保留文字搜索提示；修复后的临时边界及透传生命周期竞态回归通过（21.773s）。临时用例覆盖普通/透传大小分流、编码后扩大、未写入上游、原文不变、固定连接与执行中连接、完整及缺失工具上下文、HTTP 续链到龄、Lite 搜索、鉴权类型隔离和模型清单 ETag；验证完成后已删除本轮临时测试文件。重复或非法续链字段不得触发 HTTP 分流的临时边界验证及既有桥接判断回归通过（2.263s）。

```sh
go test ./internal/service -run 'Test(Temp|.*OpenAIWS|OpenAIGatewayService_Forward|FetchCodexModelsManifest|.*OpenAIOAuth.*WebSearch)' -count=1 -timeout=4m
go test -race ./internal/service -run 'Test(Temp|OpenAIWSConnPool|OpenAIGatewayService_ProxyResponsesWebSocketFromClient_.*(Preflight|PreviousResponseNotFound)|FetchCodexModelsManifestOAuth)' -count=1 -timeout=4m
go test -race ./internal/service -run 'Test(Temp|PassthroughLifecycle|WSPassthroughUsageMeta)' -count=1 -timeout=4m
```

最终 service/config 静态检查（Go 1.27.2 / golangci-lint v2.14.0）通过，`0 issues`。`.dev/start.sh --restart-backend` 已完成构建和本地重启，8081 健康检查与 3000 账号页面均返回 200。GitNexus impact / detect_changes 仍返回 `Transport closed`，已用源码、回归和独立审查核对范围。没有执行真实上游请求，没有提交、推送或更新线上服务。

## v0.2.173 发布前复查

基线为 `v0.2.172`（`20753aeb1`），同时检查全部未提交改动与两个新增生产文件。

- 实现与标准审查发现编码扩大时重复锚点可绕过分流检查：原文小于阈值、编码后超限，且重复 `previous_response_id` 的末值为空时，可能错误改走 HTTP。已抽出与大小无关的严格扫描，在重新选择 HTTP 前检查原始请求；重复或非法字段拒绝，合法无锚点、空字符串和 null 继续允许分流。
- 需求审查发现原生 ctx_pool 到龄轮换会误拒绝 `store=true` 的外部持久化锚点，或不必要地删除锚点。已将完整历史重建限定于 `store=false`，持久化响应保留锚点换连接续接。
- 需求审查发现 SSE 已发送心跳或事件后跳过管理员诊断。已将诊断记录与客户端 JSON 写入条件分开，并保留读取失败分类；已有流式响应不追加 JSON、不重复失败终态，事件记录阶段、原因和关闭码且不包含原始关闭原因正文。

持久化续链的两项临时验证在修复前分别复现错误关闭和锚点删除；修复后连接池、续链及 preflight 相关竞态回归通过（13.063s）。最终专项竞态回归通过（32.982s），覆盖三项修复、三种异常锚点与三种合法对照、WS/HTTP 桥接、失败不重放、已提交保活和事件后的安全诊断。临时用例及仓库外 overlay 均已清理，没有新增长期测试。

```sh
go test -race ./internal/service -run 'Test(Temp173Persistent|OpenAIWSConnPool|OpenAIGatewayService_ProxyResponsesWebSocketFromClient_.*(StoreEnabled|Preflight|PreviousResponseNotFound))' -count=1 -timeout=4m
go test -race -overlay <临时验证目录>/overlay.json ./internal/service -run 'Test(Temp173|ReviewEncodedAnchor|.*OpenAIWS.*(Fallback|Bridge)|WriteOpenAIWSFallback|OpenAIGatewayService_Forward_WSv2)' -count=1 -timeout=4m
```

最终全量静态检查（Go 1.27.2 / golangci-lint v2.14.0，`run --timeout=8m ./...`）通过，`0 issues`；包含前端资源的 `CGO_ENABLED=0 go build -tags=embed -trimpath` 通过，二进制版本确认为 `0.2.173`。16 个改动 Go 文件的格式检查及差异空白检查通过。GitNexus impact / detect_changes 仍返回 `Transport closed`，已用源码审查与回归补充核对。前端本轮未再修改，沿用此前通过的测试、ESLint 和构建。

版本更新为 `0.2.173`，中文发布说明见 `release-notes-0.2.173.md`。推送版本标签后，由既有 GitHub Actions Release 工作流构建安装包和镜像。本次没有数据库迁移，不直接更新线上运行实例。

## 参考指纹核查

Codex 源码的 `models.json` 是模型能力配置，`config/src/fingerprint.rs` 是配置摘要，均不能提供模型行为参考样本。sub4api 从 ModelTrace 的 `unified_bank.json` 更新参考库；它对 `gpt-6.1-sol` 借用 `gpt-6-sol` 类别，没有独立的 6.1 指纹。本项目不采用这种替代判定。

本轮核查 ModelTrace 的提交 `6cd4a8ff0f3f4e49d6d6969dd2fbadfa1569fde2`，仍没有 `gpt-6.1-sol` 参考样本。继续保留已固定的版本组合和“无法判定”，不构造新指纹，不以待测中转样本自证身份。

## 验证限制

初版及增量回归使用本地模拟上游。本轮仅通过线上账号 29 及其代理运行过一次极短 HTTP 探测，`instructions=""` 的 `gpt-6-astra` 请求返回 HTTP 200 和成功终态，仅排除“空 instructions 必然失败”；没有执行完整指纹挑战，不能还原此前失败。未测得线上首输出耗时、失败率或压缩/增量收益，这些改动不保证减少输入 token 或提高缓存命中率。

模型判定仅是三份完整有效样本与固定参考指纹的经验关系，不是身份认证或能力评分。参考采集与当前 Codex 协议转换、模型版本未独立校准，分数不是准确率。固定来源、profile 与限制见 `backend/internal/pkg/modeltrace/SOURCE.md`。
