package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"codearts2api/internal/openai"
)

// preflightMaxBytes 限制前置探帧最多读入的字节数。
//
// 只为了看首个事件，正常帧只有几百字节；给 2MB 上限纯属防御——上游若不
// 按 SSE 输出（比如直接吐一整个 JSON），读到这里就该停手交给下游处理。
const preflightMaxBytes = 2 << 20

// upstreamFailure 是上游业务错误的分类结果，决定给下游的状态码与错误形状。
type upstreamFailure struct {
	// Status 是建议返回给下游的 HTTP 状态码。
	Status int
	// Type / Code 是 OpenAI 错误对象里的 type 与 code。
	Type string
	Code string
	// Message 是透传给下游的错误文案。
	Message string
}

// classifyFailure 把上游错误翻译成下游能正确理解的形式。
//
// 内容审核（ModelArts.81011）必须映射成 403 + content_filter：这是请求内容
// 层面的问题，换账号、重试、等一会儿都不会变；而包成 502 会让客户端以为
// 网关故障，agent 会不断重试，把同一条敏感内容反复送审。
func classifyFailure(status int, code, message, detail string) *upstreamFailure {
	if openai.IsContentModeration(code, message) {
		if strings.TrimSpace(message) == "" {
			message = "内容未通过上游审核（ModelArts.81011），请调整提问后重试"
		}
		return &upstreamFailure{
			Status:  http.StatusForbidden,
			Type:    openai.ContentFilter,
			Code:    openai.ContentFilter,
			Message: message,
		}
	}
	if strings.TrimSpace(message) == "" {
		message = strings.TrimSpace(detail)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &upstreamFailure{
		Status:  http.StatusBadGateway,
		Type:    "upstream_error",
		Code:    code,
		Message: fmt.Sprintf("upstream %d: %s", status, message),
	}
}

// writeFailure 输出上游错误的 OpenAI 风格响应。
func writeFailure(w http.ResponseWriter, fail *upstreamFailure) {
	status := fail.Status
	if status == 0 {
		status = http.StatusBadGateway
	}
	writeError(w, status, fail.Message, fail.Type, fail.Code)
}

// preflightStream 在把流交给客户端之前先看一眼首个事件。
//
// 上游经常以 HTTP 200 返回流，却把错误放在第一帧里（内容审核 81011 就是这种
// 形态）。如果先写了 200 响应头再转发，客户端永远收不到「这是错误」的信号，
// 只能看到一个空回复。这里先读首帧判断，命中错误就整个请求按失败处理。
//
// 返回的 ReadCloser 会重放已读字节；没有错误时行为与原始流完全一致。
func preflightStream(body io.ReadCloser) (io.ReadCloser, *upstreamFailure) {
	br := bufio.NewReaderSize(body, 64*1024)
	var prefix bytes.Buffer

	for prefix.Len() < preflightMaxBytes {
		line, err := br.ReadString('\n')
		if line != "" {
			prefix.WriteString(line)
		}

		if payload, ok := ssePayload(line); ok {
			// [DONE] 单独成帧说明上游没吐任何内容，交给下游按空响应处理。
			if payload == "[DONE]" {
				break
			}
			var chunk map[string]any
			if jsonErr := json.Unmarshal([]byte(payload), &chunk); jsonErr == nil {
				if code, message, found := openai.ExtractEmbeddedError(chunk); found {
					_ = body.Close()
					return nil, classifyFailure(http.StatusOK, code, message, payload)
				}
			}
			break
		}
		if err != nil {
			break
		}
	}
	return &replayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), br),
		Closer: body,
	}, nil
}

// replayReadCloser 把「已读前缀 + 剩余流」重新拼成一个可关闭的 Reader。
type replayReadCloser struct {
	io.Reader
	io.Closer
}

// ssePayload 提取 "data:" 后的负载；非 data 行（注释、event 行）返回 ok=false。
func ssePayload(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")), true
}
