package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// 缓存已配置的时区规则，避免每次请求读取 zoneinfo 文件；限制配置反复变更产生的条目数。
const codexEnvironmentLocationCacheLimit = 128

var codexEnvironmentLocations = struct {
	sync.Mutex                           // 保护时区规则缓存。
	values     map[string]*time.Location // 已加载的 IANA 时区规则。
}{values: make(map[string]*time.Location)}

// codexTimeReplacement 记录原文中需要局部替换的区间。
type codexTimeReplacement struct {
	start int    // 原文中的起始字节位置。
	end   int    // 原文中的结束字节位置，不含此位置。
	value []byte // 替换后的内容。
}

// rewriteCodexEnvironmentTime 仅为 Codex 协议账号改写客户端环境块，未明确配置时保留原始请求。
func (s *OpenAIGatewayService) rewriteCodexEnvironmentTime(ctx context.Context, account *Account, body []byte, now time.Time) ([]byte, error) {
	if account == nil || !account.IsOpenAI() || !account.UsesOpenAICodexProtocol() {
		return body, nil
	}

	// 只读取消息正文和 instructions，不遍历工具结果、工具定义、附件或 assistant 历史。
	var texts []gjson.Result
	addText := func(value gjson.Result) {
		if value.Type == gjson.String && strings.HasPrefix(strings.TrimSpace(value.Str), "<environment_context>") {
			texts = append(texts, value)
		}
	}
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "instructions":
			addText(value)
		case "input":
			if value.Type == gjson.String {
				addText(value)
			} else if value.IsArray() {
				value.ForEach(func(_, message gjson.Result) bool {
					kind := message.Get("type").String()
					if kind != "" && kind != "message" {
						return true
					}
					switch message.Get("role").String() {
					case "user", "system", "developer":
						content := message.Get("content")
						if content.IsArray() {
							content.ForEach(func(_, part gjson.Result) bool {
								if kind := part.Get("type").String(); kind == "input_text" || kind == "text" {
									addText(part.Get("text"))
								}
								return true
							})
						} else {
							addText(content)
						}
					}
					return true
				})
			}
		}
		return true
	})
	if len(texts) == 0 {
		return body, nil
	}
	location, err := s.codexEnvironmentLocation(ctx, account)
	if err != nil || location == nil {
		return body, err
	}
	date := now.In(location).Format(time.DateOnly)
	var edits []codexTimeReplacement
	for _, text := range texts {
		rewritten := rewriteCodexEnvironmentText(text.Str, location.String(), date)
		if rewritten == text.Str {
			continue
		}
		encoded, err := json.Marshal(rewritten)
		if err != nil {
			return nil, fmt.Errorf("encode Codex environment time: %w", err)
		}
		edits = append(edits, codexTimeReplacement{start: text.Index, end: text.Index + len(text.Raw), value: encoded})
	}
	return applyCodexTimeReplacements(body, edits), nil
}

// codexEnvironmentLocation 优先读取实际代理的已保存时区，再回退到明确保存的全局时区。
func (s *OpenAIGatewayService) codexEnvironmentLocation(ctx context.Context, account *Account) (*time.Location, error) {
	timezone := ""
	if account.ProxyID != nil && account.Proxy != nil {
		timezone = account.Proxy.Timezone
	}
	if timezone == "" && s.settingService != nil {
		settings, err := s.settingService.GetGatewayTimezoneSettings(ctx)
		if err != nil {
			return nil, fmt.Errorf("read Codex environment timezone: %w", err)
		}
		if settings.IsSet {
			timezone = settings.Timezone
		}
	}
	if timezone == "" {
		return nil, nil
	}
	if timezone == "Local" {
		return nil, fmt.Errorf("codex environment timezone must be an IANA timezone")
	}
	location, err := loadCodexEnvironmentLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load Codex environment timezone: %w", err)
	}
	return location, nil
}

// loadCodexEnvironmentLocation 复用时区规则，缓存达到上限时清空后重新按需加载。
func loadCodexEnvironmentLocation(timezone string) (*time.Location, error) {
	codexEnvironmentLocations.Lock()
	defer codexEnvironmentLocations.Unlock()
	if location := codexEnvironmentLocations.values[timezone]; location != nil {
		return location, nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, err
	}
	if len(codexEnvironmentLocations.values) >= codexEnvironmentLocationCacheLimit {
		clear(codexEnvironmentLocations.values)
	}
	codexEnvironmentLocations.values[timezone] = location
	return location, nil
}

// rewriteCodexEnvironmentText 只识别独立环境块的直接子字段，保留引用、嵌套内容和不完整的环境块。
func rewriteCodexEnvironmentText(text, timezone, date string) string {
	decoder := xml.NewDecoder(strings.NewReader(text))
	depth, valueStart := 0, 0
	field := ""
	var edits []codexTimeReplacement
	for {
		tokenStart := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 非客户端固定格式（例如用户引用或损坏片段）整体保留，避免误改正文。
			return text
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 && (token.Name.Local != "environment_context" || token.Name.Space != "" || len(token.Attr) != 0) {
				return text
			}
			if depth == 2 && token.Name.Space == "" && len(token.Attr) == 0 && (token.Name.Local == "timezone" || token.Name.Local == "current_date") {
				field, valueStart = token.Name.Local, int(decoder.InputOffset())
			} else {
				field = ""
			}
		case xml.EndElement:
			if depth == 2 && field != "" {
				original := text[valueStart:tokenStart]
				replacement := timezone
				valid := strings.TrimSpace(original) != "" && !strings.ContainsAny(original, "<>")
				if field == "current_date" {
					replacement = date
					_, err := time.Parse(time.DateOnly, strings.TrimSpace(original))
					valid = valid && err == nil
				}
				if valid && original != replacement {
					edits = append(edits, codexTimeReplacement{start: valueStart, end: tokenStart, value: []byte(replacement)})
				}
			}
			field = ""
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(token)) != "" {
				return text
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			return text
		}
	}
	if len(edits) == 0 {
		return text
	}
	return string(applyCodexTimeReplacements([]byte(text), edits))
}

// applyCodexTimeReplacements 按原文顺序一次拼接所有替换，未命中时不复制请求。
func applyCodexTimeReplacements(body []byte, edits []codexTimeReplacement) []byte {
	if len(edits) == 0 {
		return body
	}
	size := len(body)
	for _, edit := range edits {
		size += len(edit.value) - (edit.end - edit.start)
	}
	result := make([]byte, 0, size)
	position := 0
	for _, edit := range edits {
		result = append(result, body[position:edit.start]...)
		result = append(result, edit.value...)
		position = edit.end
	}
	return append(result, body[position:]...)
}
