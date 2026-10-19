固定来源：[xqy2006/ModelTrace 55a2e4a55170423b484d701e9a82ab62b268c811](https://github.com/xqy2006/ModelTrace/tree/55a2e4a55170423b484d701e9a82ab62b268c811)（MIT，见 modeltrace_LICENSE）。分类器经相邻参考实现的 Go 移植复用；不包含自动更新、打票、状态复用或自动停调。

固定请求 profile `codex-number-selection-v1` 使用原项目 `challenge_suite.py` 的 `environment-05`：三个英文模板及对应 297/315/331 数量，清洁上下文，无 system/user prefix，不提供工具，不覆盖 temperature/top_p/reasoning。每份样本独立选择模板并使用新的线程、回合与缓存键。

可比依据为同提交 `data/gpt_reference.jsonl` 的 `provider=codex`、`temperature=provider_default`、`wrapper_transport=clean`、`condition_id=environment-05` 记录以及 `enrollment.py` 的默认参数请求。诊断用 Codex Responses 表达相同 user 内容，`instructions` 为空，`store=false`，`stream=true`；HTTP/WS 路径由账号当前配置决定。参考采集通过 Chat Completions 接入，具体上游协议转换及当前模型版本未独立校准，因此只能展示本次完整有效样本与固定参考指纹的经验关系，不能承诺身份认证或准确率。

新增严格数量、格式、重复和明显规律检查；三份有效独立样本须候选一致才给出经验线索。候选分数是三个闭集相对分数的均值，不是已校准概率。未覆盖模型直接无法判定，不向上游采样。
