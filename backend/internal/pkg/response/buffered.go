package response

import (
	"bytes"
	"strings"

	"github.com/gin-gonic/gin"
)

// BufferedWriter 暂存 JSON 响应，并保持明确声明的 SSE 响应可以实时发送。
type BufferedWriter struct {
	gin.ResponseWriter              // 原始 HTTP 响应。
	Body               bytes.Buffer // 尚未发送的响应内容。
	Code               int          // 暂存的 HTTP 状态。
	streaming          bool         // 当前响应是否已切换为 SSE 直写。
	written            bool         // 处理器是否已写入响应。
}

// NewBufferedWriter 创建不立即发送 JSON 的写入器。
func NewBufferedWriter(writer gin.ResponseWriter) *BufferedWriter {
	return &BufferedWriter{ResponseWriter: writer, Code: 200}
}

// Write 暂存 JSON；SSE 在首次写入时切换为直写。
func (w *BufferedWriter) Write(data []byte) (int, error) {
	w.beginStream()
	w.written = true
	if w.streaming {
		return w.ResponseWriter.Write(data)
	}
	return w.Body.Write(data)
}

// WriteString 按相同策略处理字符串内容。
func (w *BufferedWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

// WriteHeader 在处理器尚未开始写入时记录响应状态。
func (w *BufferedWriter) WriteHeader(code int) {
	if !w.written {
		w.Code = code
	}
	if w.streaming {
		w.ResponseWriter.WriteHeader(code)
	}
}

// WriteHeaderNow 保留延迟发送，流式响应除外。
func (w *BufferedWriter) WriteHeaderNow() {
	w.beginStream()
	w.written = true
	if w.streaming {
		w.ResponseWriter.WriteHeaderNow()
	}
}

// Status 返回当前 HTTP 状态。
func (w *BufferedWriter) Status() int { return w.Code }

// Size 返回已生成的响应大小。
func (w *BufferedWriter) Size() int {
	if w.streaming {
		return w.ResponseWriter.Size()
	}
	return w.Body.Len()
}

// Written 报告处理器是否已开始生成响应。
func (w *BufferedWriter) Written() bool { return w.written }

// Flush 仅刷新明确声明的 SSE，避免 JSON 脱敏前被提前发送。
func (w *BufferedWriter) Flush() {
	w.beginStream()
	if w.streaming {
		w.ResponseWriter.Flush()
	}
}

// Streaming 报告响应是否已经由 SSE 直接发送。
func (w *BufferedWriter) Streaming() bool { return w.streaming }

// beginStream 在 SSE 首次发送时保留状态和已经生成的内容。
func (w *BufferedWriter) beginStream() {
	if w.streaming || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		return
	}
	w.streaming = true
	w.ResponseWriter.WriteHeader(w.Code)
	if w.Body.Len() > 0 {
		_, _ = w.ResponseWriter.Write(w.Body.Bytes())
		w.Body.Reset()
	}
}
