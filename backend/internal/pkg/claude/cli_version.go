package claude

import (
	"log/slog"
	"os"
	"strings"
)

// CLIVersionEnv 保留旧部署配置入口，但只接受已验证的固定版本。
const CLIVersionEnv = "SUB2API_CLAUDE_CLI_VERSION"

// resolvedCLIVersion 在包初始化时解析一次。
//
// ⚠️ 故意不做成"每次调用读一次环境变量"：伪装身份必须在一个进程的生命周期内保持恒定。
// User-Agent 头与请求体 billing attribution 块里的 cc_version 由不同代码路径写入，
// 若两次读到不同的值（例如进程运行中有人改了环境变量），同一个请求就会自相矛盾，
// 被上游判为非正版客户端。
var resolvedCLIVersion = resolveCLIVersion(os.Getenv(CLIVersionEnv))

// CLIVersion 返回对外伪装的 Claude Code CLI 版本号（三段 semver）。
//
// 所有需要该版本号的位置都必须走本函数，不要直接引用 CLICurrentVersion——
// 后者只是"没有覆盖时的内置基线"。
func CLIVersion() string {
	return resolvedCLIVersion
}

// IsSupportedCLIVersion 只接受固定版本，禁止设置、自动同步或环境变量单独升级版本声明。
func IsSupportedCLIVersion(version string) bool {
	return strings.TrimSpace(version) == CLICurrentVersion
}

// resolveCLIVersion 把环境变量的原始值解析成可用的版本号。
// 空值静默回落（未配置是正常状态）；非空但不合法则回落并告警——
// 静默忽略一个显式配置会让运维以为已经生效。
func resolveCLIVersion(raw string) string {
	version := strings.TrimSpace(raw)
	if version == "" {
		return CLICurrentVersion
	}
	if !IsSupportedCLIVersion(version) {
		slog.Warn("ignoring invalid Claude CLI version override; falling back to the built-in pin",
			"env", CLIVersionEnv,
			"value", version,
			"builtin", CLICurrentVersion,
			"requirement", "must equal the built-in pin")
		return CLICurrentVersion
	}
	return version
}
