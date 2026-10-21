package anthropicfp

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	dateParserPrompt  = "You are a date/time parser that converts natural language into ISO 8601 format."
	dateParserFooter  = "\n\nParse the user's input into ISO 8601 format. Return ONLY the formatted string, or \"INVALID\" if the input is incomplete or unparseable."
	schedulePrompt    = "# Schedule Cloud Agents\n\nYou are helping the user schedule, update, list, or run **cloud** Claude Code agents."
	environmentPrompt = "# Environment\nYou have been invoked in the following environment:"
)

var (
	automaticDateLine    = regexp.MustCompile(`(?m)^Today['’ʼʹ]s date is (?:\d{4}-\d{2}-\d{2}|\d{4}/\d{2}/\d{2})\.$`)
	changedDateLine      = regexp.MustCompile(`(?m)^The date has changed\. Today['’ʼʹ]s date is now (?:\d{4}-\d{2}-\d{2}|\d{4}/\d{2}/\d{2})\. No need to announce the new date — the user's own clock shows it\.$`)
	dateParserContext    = regexp.MustCompile(`\ACurrent context:\n- Current date and time: ([^\n]+) \(UTC\)\n- Local timezone: [+-]\d{2}:\d{2}\n- Day of week: [A-Za-z]+\n\nUser input: "`)
	dateTimeFormat       = regexp.MustCompile(`\AYYYY-MM-DDTHH:MM:SS[+-]\d{2}:\d{2} \(full date-time with timezone\)\z`)
	scheduleZone         = regexp.MustCompile(`(?m)^The user's local timezone is \*\*([^*\n]+)\*\*\. Cron expressions and `)
	scheduleTime         = regexp.MustCompile(`(?m)^When /schedule was invoked it was \*\*[^\n]*?\*\* \([^\n]*?\) / \*\*([^*\n]+)\*\* UTC\.`)
	environmentOSVersion = regexp.MustCompile(`(?m)^ - OS Version: [^\n]*(?:\n|\z)`)
)

type clientEnvironmentText struct {
	path   string // 文本在请求 JSON 中的路径。
	text   string // 待检查的 system 或 user 文本。
	system bool   // 是否属于系统指令，包含兼容入口搬移的系统指令。
}

// clientEnvironmentTexts 收集可包含自动模板的文本，不进入工具参数、工具结果或助手消息。
func clientEnvironmentTexts(body []byte) []clientEnvironmentText {
	var fields []clientEnvironmentText
	appendContent := func(content gjson.Result, path string, system bool) {
		if content.Type == gjson.String {
			fields = append(fields, clientEnvironmentText{path, content.String(), system})
			return
		}
		for i, block := range content.Array() {
			if block.Get("type").String() == "text" && block.Get("text").Type == gjson.String {
				fields = append(fields, clientEnvironmentText{fmt.Sprintf("%s.%d.text", path, i), block.Get("text").String(), system})
			}
		}
	}
	appendContent(gjson.GetBytes(body, "system"), "system", true)
	for i, message := range gjson.GetBytes(body, "messages").Array() {
		if message.Get("role").String() == "user" {
			appendContent(message.Get("content"), fmt.Sprintf("messages.%d.content", i), false)
		}
	}
	return fields
}

// NormalizeClientEnvironment 将已确认的自动时间模板统一为 UTC，保留其它正文的原始字节。
func NormalizeClientEnvironment(body []byte, now time.Time) ([]byte, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, fmt.Errorf("normalize client environment: invalid JSON object")
	}
	fields := clientEnvironmentTexts(body)
	dateParser := false
	for i := range fields {
		field := &fields[i]
		if strings.HasPrefix(field.text, "[System Instructions]\n") {
			field.system = true
		}
		if field.system && strings.HasPrefix(strings.TrimPrefix(field.text, "[System Instructions]\n"), dateParserPrompt) {
			dateParser = true
		}
	}
	out := body
	for _, field := range fields {
		text := field.text
		prefix := ""
		if field.system && strings.HasPrefix(text, "[System Instructions]\n") {
			prefix = "[System Instructions]\n"
			text = strings.TrimPrefix(text, prefix)
		}
		var err error
		switch {
		case strings.HasPrefix(text, schedulePrompt):
			text, err = normalizeScheduleContext(text)
		case dateParser && !field.system:
			text, err = normalizeDateParserContext(text)
		default:
			text = normalizeAutomaticDates(text, now.UTC().Format("2006-01-02"), field.system)
			text = normalizeEnvironmentVersion(text)
		}
		if err != nil {
			return nil, err
		}
		text = prefix + text
		if text != field.text {
			out, err = sjson.SetBytes(out, field.path, text)
			if err != nil {
				return nil, fmt.Errorf("normalize %s: %w", field.path, err)
			}
		}
	}
	// 2.1.299 的可选逐轮 timing 可能携带本地 UTC 偏移；保留其它 output_config 控制项。
	for i, message := range gjson.GetBytes(out, "messages").Array() {
		if message.Get("output_config.timing").Exists() {
			var err error
			out, err = sjson.DeleteBytes(out, fmt.Sprintf("messages.%d.output_config.timing", i))
			if err != nil {
				return nil, fmt.Errorf("remove client timing: %w", err)
			}
		}
	}
	return out, nil
}

// normalizeAutomaticDates 只改系统指令或 reminder 内完整匹配的自动日期行。
func normalizeAutomaticDates(text, date string, system bool) string {
	normalize := func(value string) string {
		value = automaticDateLine.ReplaceAllString(value, "Today's date is "+date+".")
		return changedDateLine.ReplaceAllString(value, "The date has changed. Today's date is now "+date+". No need to announce the new date — the user's own clock shows it.")
	}
	text = normalizeClientReminder(text, normalize)
	if system {
		text = normalize(text)
	}
	return text
}

// normalizeClientReminder 只处理独立 reminder 文本块，避免改写普通对话中引用的模板。
func normalizeClientReminder(text string, normalize func(string) string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || systemReminderRegex.FindString(trimmed) != trimmed {
		return text
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "<system-reminder>"), "</system-reminder>")
	normalized := "<system-reminder>" + normalize(inner) + "</system-reminder>"
	return strings.Replace(text, trimmed, normalized, 1)
}

// normalizeEnvironmentVersion 删除已确认环境模板的详细系统版本，保留工具执行所需的平台和路径。
func normalizeEnvironmentVersion(text string) string {
	normalize := func(value string) string {
		if strings.HasPrefix(strings.TrimSpace(value), environmentPrompt) {
			return environmentOSVersion.ReplaceAllString(value, "")
		}
		return value
	}
	text = normalizeClientReminder(text, normalize)
	return normalize(text)
}

// normalizeDateParserContext 同步日期解析的偏移、星期及输出格式，不改 User input。
func normalizeDateParserContext(text string) (string, error) {
	match := dateParserContext.FindStringSubmatchIndex(text)
	if match == nil || !strings.HasSuffix(text, dateParserFooter) {
		return text, nil
	}
	formatStart := strings.LastIndex(text, "\"\n\nOutput format: ")
	if formatStart < match[1] {
		return text, nil
	}
	format := text[formatStart+len("\"\n\nOutput format: ") : len(text)-len(dateParserFooter)]
	switch {
	case dateTimeFormat.MatchString(format):
		format = "YYYY-MM-DDTHH:MM:SS+00:00 (full date-time with timezone)"
	case format == "YYYY-MM-DD (date only, no time)":
	default:
		return "", fmt.Errorf("unsupported automatic date parser format")
	}
	instant, err := time.Parse(time.RFC3339Nano, text[match[2]:match[3]])
	if err != nil {
		return "", fmt.Errorf("parse automatic date context: %w", err)
	}
	prefix := fmt.Sprintf("Current context:\n- Current date and time: %s (UTC)\n- Local timezone: +00:00\n- Day of week: %s\n\nUser input: \"", instant.UTC().Format(time.RFC3339Nano), instant.UTC().Weekday())
	return prefix + text[match[1]:formatStart] + "\"\n\nOutput format: " + format + dateParserFooter, nil
}

// normalizeScheduleContext 只改已确认日程模板的五处时区及本地时间，保护末尾用户请求。
func normalizeScheduleContext(text string) (string, error) {
	if !strings.HasPrefix(text, schedulePrompt) {
		return text, nil
	}
	head, tail := text, ""
	if index := strings.Index(text, "\n## User Request\n\nThe user said: \""); index >= 0 {
		head, tail = text[:index], text[index:]
	}
	zone := scheduleZone.FindStringSubmatch(head)
	clock := scheduleTime.FindStringSubmatch(head)
	if zone == nil || clock == nil {
		return text, nil
	}
	replacements := [][2]string{
		{"The user's local timezone is **" + zone[1] + "**.", "The user's local timezone is **UTC**."},
		{"confirm with them: \"9am " + zone[1] + " = Xam UTC,", "confirm with them: \"9am UTC = Xam UTC,"},
		{"The user's timezone is " + zone[1] + ". When they say a time", "The user's timezone is UTC. When they say a time"},
		{"Always confirm the conversion: \"9am " + zone[1] + " = Xam UTC.\"", "Always confirm the conversion: \"9am UTC = Xam UTC.\""},
	}
	for _, replacement := range replacements {
		if !strings.Contains(head, replacement[0]) {
			return text, nil
		}
	}
	instant, err := time.Parse(time.RFC3339Nano, clock[1])
	if err != nil {
		return "", fmt.Errorf("parse automatic schedule time: %w", err)
	}
	for _, replacement := range replacements {
		head = strings.ReplaceAll(head, replacement[0], replacement[1])
	}
	utcTime := instant.UTC().Format(time.RFC3339Nano)
	head = scheduleTime.ReplaceAllString(head, "When /schedule was invoked it was **"+utcTime+"** (UTC) / **"+utcTime+"** UTC.")
	return head + tail, nil
}
