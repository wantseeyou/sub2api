package repository

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var codexHTTPZstdEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
})

// compressOpenAICodexRequest 压缩可重读的 Codex HTTP 请求副本，保留原请求及重试输入。
func compressOpenAICodexRequest(request *http.Request) (*http.Request, bool, error) {
	if request == nil || request.URL == nil || request.Body == nil || request.GetBody == nil ||
		request.Method != http.MethodPost || request.URL.Scheme != "https" ||
		!strings.EqualFold(request.URL.Hostname(), "chatgpt.com") || request.URL.Path != "/backend-api/codex/responses" {
		return request, false, nil
	}
	mediaType, _, _ := strings.Cut(request.Header.Get("Content-Type"), ";")
	encoding := strings.TrimSpace(request.Header.Get("Content-Encoding"))
	if !strings.EqualFold(strings.TrimSpace(mediaType), "application/json") || (encoding != "" && !strings.EqualFold(encoding, "identity")) {
		return request, false, nil
	}
	encoder, err := codexHTTPZstdEncoder()
	if err != nil {
		return nil, false, fmt.Errorf("initialize Codex compression: %w", err)
	}
	replay, err := request.GetBody()
	if err != nil {
		return nil, false, fmt.Errorf("open Codex compression input: %w", err)
	}
	payload, readErr := io.ReadAll(replay)
	closeErr := replay.Close()
	if readErr != nil {
		return nil, false, fmt.Errorf("read Codex compression input: %w", readErr)
	}
	if closeErr != nil {
		return nil, false, fmt.Errorf("close Codex compression input: %w", closeErr)
	}
	encoded := encoder.EncodeAll(payload, nil)
	wire := request.Clone(request.Context())
	wire.Body = io.NopCloser(bytes.NewReader(encoded))
	wire.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
	wire.ContentLength = int64(len(encoded))
	wire.TransferEncoding = nil
	wire.Header.Del("Content-Length")
	wire.Header.Set("Content-Encoding", "zstd")
	return wire, true, nil
}
