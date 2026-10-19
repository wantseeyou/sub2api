package service

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeOpenAIOAuthWebSearchContentTypes 为普通 Codex 账号保留文字网页搜索，明确拒绝仅图片搜索。
func normalizeOpenAIOAuthWebSearchContentTypes(account *Account, body []byte) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAIOAuthLike() || account.IsOpenAIAgentIdentity() || !bytes.Contains(body, []byte("search_content_types")) {
		return body, false, nil
	}
	paths := []string{"tools"}
	for i, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == openAIAdditionalToolsItemType {
			paths = append(paths, fmt.Sprintf("input.%d.tools", i))
		}
	}
	next := body
	changed := false
	for _, path := range paths {
		for i, tool := range gjson.GetBytes(body, path).Array() {
			if !isOpenAIWebSearchToolType(tool.Get("type").String()) {
				continue
			}
			contentTypes := tool.Get("search_content_types")
			if !contentTypes.IsArray() {
				continue
			}
			hasImage, hasText := false, false
			filtered := make([]any, 0, len(contentTypes.Array()))
			for _, contentType := range contentTypes.Array() {
				if contentType.Type == gjson.String && contentType.String() == "image" {
					hasImage = true
					continue
				}
				hasText = hasText || contentType.Type == gjson.String && contentType.String() == "text"
				filtered = append(filtered, contentType.Value())
			}
			if !hasImage {
				continue
			}
			if !hasText {
				return body, false, errors.New("this Codex account supports text web search only; use web_search with text content instead of image-only web search")
			}
			var err error
			next, err = sjson.SetBytes(next, fmt.Sprintf("%s.%d.search_content_types", path, i), filtered)
			if err != nil {
				return body, false, fmt.Errorf("normalize web_search content types: %w", err)
			}
			changed = true
		}
	}
	return next, changed, nil
}

// normalizeOpenAIOAuthWebSearchManifest 对齐模型清单与普通 Codex 账号实际支持的搜索能力。
func normalizeOpenAIOAuthWebSearchManifest(account *Account, body []byte) ([]byte, error) {
	if account == nil || !account.IsOpenAIOAuthLike() || account.IsOpenAIAgentIdentity() {
		return body, nil
	}
	next := body
	for i, model := range gjson.GetBytes(body, "models").Array() {
		if model.Get("web_search_tool_type").String() != "text_and_image" {
			continue
		}
		var err error
		next, err = sjson.SetBytes(next, fmt.Sprintf("models.%d.web_search_tool_type", i), "text")
		if err != nil {
			return nil, fmt.Errorf("normalize Codex web search capability: %w", err)
		}
	}
	return next, nil
}
