package groupview

import (
	"fmt"
	"strings"
)

// snapshotNumber 仅读取 JSON 数字，避免异常快照触发 SQL 类型转换错误。
func snapshotNumber(expression string) string {
	return fmt.Sprintf("(CASE WHEN jsonb_typeof(%[1]s) = 'number' THEN (%[1]s #>> '{}')::numeric END)", expression)
}

// snapshotUnixTime 将有效 Unix 秒转换为 JSON 时间，缺失或越界时保留空值。
func snapshotUnixTime(expression string) string {
	number := snapshotNumber(expression)
	return fmt.Sprintf("(CASE WHEN %[1]s BETWEEN 0 AND 253402300799 THEN to_jsonb(to_timestamp((%[1]s)::double precision)) END)", number)
}

// quotaProjection 将账号已有额度快照投影为白名单窗口数组，不执行探测或状态更新。
func quotaProjection() string {
	rows := make([]string, 0, 32)
	// addWindow 记录固定窗口字段，所有 SQL 表达式均来自本文件。
	addWindow := func(name, used, scale, resetsAt, updatedAt string) {
		rows = append(rows, fmt.Sprintf("('%s', %s, %s::numeric, %s, %s)", name, used, scale, resetsAt, updatedAt))
	}

	for _, window := range []string{"5h", "7d"} {
		addWindow("openai_"+window, "a.extra->'codex_"+window+"_used_percent'", "1",
			"a.extra->'codex_"+window+"_reset_at'", "a.extra->'codex_usage_updated_at'")
	}
	addWindow("anthropic_5h", "a.extra->'session_window_utilization'", "100",
		"to_jsonb(a.session_window_end)", "a.extra->'passive_usage_sampled_at'")
	for _, window := range []string{"7d", "7d_oi"} {
		addWindow("anthropic_"+window, "a.extra->'passive_usage_"+window+"_utilization'", "100",
			snapshotUnixTime("a.extra->'passive_usage_"+window+"_reset'"), "a.extra->'passive_usage_sampled_at'")
	}

	for _, provider := range []string{"kimi", "zhipu", "minimax", "command_code", "cline"} {
		for _, window := range []string{"5h", "weekly", "monthly"} {
			addWindow(provider+"_"+window, "a.extra->'"+provider+"_"+window+"_used_percent'", "1",
				"a.extra->'"+provider+"_"+window+"_reset_at'", "a.extra->'"+provider+"_usage_updated_at'")
		}
	}

	for _, window := range []string{"five_hour", "seven_day"} {
		path := "a.extra #> '{ollama_cloud_usage_snapshot,data," + window
		addWindow("ollama_"+window, path+",used_percent}'", "1", path+",reset_at}'",
			"a.extra #> '{ollama_cloud_usage_snapshot,fetched_at}'")
	}
	for _, window := range []struct {
		name   string // 对外窗口名称及旧快照的字段后缀。
		stored string // 独立快照中的窗口名称。
	}{{"5h", "rolling"}, {"weekly", "weekly"}, {"monthly", "monthly"}} {
		path := "a.extra #> '{opencode_go_usage_snapshot,data," + window.stored
		present := "jsonb_typeof(" + path + ",percent}') = 'number'"
		used := "CASE WHEN " + present + " THEN " + path + ",percent}' ELSE a.extra->'opencode_go_" + window.name + "_used_percent' END"
		reset := "CASE WHEN " + present + " THEN " + path + ",resets_at}' ELSE a.extra->'opencode_go_" + window.name + "_reset_at' END"
		updated := "CASE WHEN " + present + " THEN a.extra #> '{opencode_go_usage_snapshot,fetched_at}' ELSE a.extra->'opencode_go_usage_updated_at' END"
		addWindow("opencode_go_"+window.name, used, "1", reset, updated)
	}

	for _, window := range []string{"requests", "tokens"} {
		path := "a.extra #> '{grok_usage_snapshot," + window
		limit := snapshotNumber(path + ",limit}'")
		remaining := snapshotNumber(path + ",remaining}'")
		used := fmt.Sprintf("CASE WHEN %[1]s > 0 THEN to_jsonb(100 * (1 - %[2]s / NULLIF(%[1]s, 0))) END", limit, remaining)
		reset := "COALESCE(NULLIF(" + path + ",reset_at}', 'null'::jsonb), " + snapshotUnixTime(path+",reset_unix}'") + ")"
		addWindow("grok_"+window, used, "1", reset, "a.extra #> '{grok_usage_snapshot,updated_at}'")
	}
	addWindow("grok_weekly", "a.extra #> '{grok_billing_snapshot,usage_percent}'", "1",
		"a.extra #> '{grok_billing_snapshot,period_end}'",
		"COALESCE(NULLIF(a.extra #> '{grok_billing_snapshot,weekly_updated_at}', 'null'::jsonb), a.extra #> '{grok_billing_snapshot,updated_at}')")
	addWindow("grok_monthly", "a.extra #> '{grok_billing_snapshot,used_percent}'", "1",
		"a.extra #> '{grok_billing_snapshot,billing_period_end}'",
		"COALESCE(NULLIF(a.extra #> '{grok_billing_snapshot,monthly_updated_at}', 'null'::jsonb), a.extra #> '{grok_billing_snapshot,updated_at}')")

	return `(SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'name', quota_window.name,
        'used_percent', CASE WHEN jsonb_typeof(quota_window.used) = 'number'
            THEN LEAST(100::numeric, GREATEST(0::numeric, (quota_window.used #>> '{}')::numeric * quota_window.scale)) END,
        'resets_at', CASE WHEN jsonb_typeof(quota_window.resets_at) = 'string' THEN quota_window.resets_at END,
        'updated_at', CASE WHEN jsonb_typeof(quota_window.updated_at) = 'string' THEN quota_window.updated_at END
    ) ORDER BY quota_window.name), '[]'::jsonb)
    FROM (VALUES ` + strings.Join(rows, ",\n        ") + `) AS quota_window(name, used, scale, resets_at, updated_at)
    WHERE jsonb_typeof(quota_window.used) = 'number')`
}
