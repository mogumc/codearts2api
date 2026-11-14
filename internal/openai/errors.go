package openai

import (
	"encoding/json"
	"strings"
)

// ContentFilter 是内容审核错误对外的 OpenAI 错误码与错误类型。
//
// 采用 OpenAI 生态里约定的 content_filter 而不是透传上游的 81011：
// 客户端（OpenCode / Cline / Claude Code 等）普遍按这个值决定「别重试，
// 换个说法」；透传 81011 只会被当成一次普通的 4xx/5xx 而无限重试。
const ContentFilter = "content_filter"

// maxErrorMessageLen 限制透传的错误文案长度。
//
// 上游文案可能很长（含大段提示），下游日志/终端塞不下；超出部分截断，
// 保留开头足以定位问题。
const maxErrorMessageLen = 1000

// IsContentModeration 判断上游错误是否为内容审核命中。
//
// 华为云 ModelArts 对「输入或输出含敏感信息」返回 HTTP 403 + ModelArts.81011。
// CodeArts 在它外面又包了一层，错误码形如 InferHub.ModelArts.81011.403，
// 文案也不固定：可能是官方的 "May contain sensitive information"，
// 也可能是 CodeArts Agent 的「抱歉，我无法提供关于您提问内容的建议…」。
// 三种特征都要认，否则一次审核命中会被当成网关故障。
func IsContentModeration(code, message string) bool {
	if strings.Contains(code, "81011") {
		return true
	}
	if strings.Contains(strings.ToLower(message), "sensitive information") {
		return true
	}
	return strings.Contains(message, "无法提供关于您提问内容")
}

// ExtractUpstreamError 从上游错误响应体里取出错误码与错误信息。
//
// 上游有两种错误形状：
//   - 网关层：{"error_code":"InferHub.ModelArts.81011.403","error_msg":"..."}
//   - OpenAI 层：{"error":{"message":"...","code":"..."}}
//
// 都解析不出来时把原文当作 message 返回，保证客户端至少能看到上游说了什么。
func ExtractUpstreamError(raw []byte) (code, message string) {
	var payload struct {
		ErrorCode string `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
		Error     struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		code = strings.TrimSpace(payload.ErrorCode)
		message = strings.TrimSpace(payload.ErrorMsg)
		if message == "" {
			message = strings.TrimSpace(payload.Error.Message)
		}
		if code == "" {
			if v, ok := payload.Error.Code.(string); ok {
				code = strings.TrimSpace(v)
			}
		}
	}
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	return code, truncateMessage(message)
}

// ExtractEmbeddedError 检查一个上游 SSE 数据帧里是否内嵌了业务错误。
//
// 上游经常以 HTTP 200 返回流，却把错误放在第一帧里；帧形状是
// {"text":"[DONE]","error_code":"...","error_msg":"..."}，因此必须在把
// text 当成正常结束标记之前先看 error_code。
func ExtractEmbeddedError(chunk map[string]any) (code, message string, found bool) {
	if chunk == nil {
		return "", "", false
	}
	if raw, ok := chunk["error_code"].(string); ok && raw != "" && raw != "0" {
		message, _ = chunk["error_msg"].(string)
		return raw, truncateMessage(strings.TrimSpace(message)), true
	}
	if raw, ok := chunk["error_msg"].(string); ok && raw != "" {
		return "", truncateMessage(strings.TrimSpace(raw)), true
	}
	if obj, ok := chunk["error"].(map[string]any); ok {
		message, _ := obj["message"].(string)
		code, _ := obj["code"].(string)
		if message != "" || code != "" {
			return code, truncateMessage(strings.TrimSpace(message)), true
		}
	}
	return "", "", false
}

// truncateMessage 按字节截断过长文案，避免把整段上游提示塞给客户端。
func truncateMessage(s string) string {
	if len(s) <= maxErrorMessageLen {
		return s
	}
	return s[:maxErrorMessageLen] + "…"
}
