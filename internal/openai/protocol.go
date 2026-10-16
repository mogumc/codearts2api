// Package openai 处理 OpenAI 兼容协议的请求/响应转换。
package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// NormalizeFinishReason 把 CodeArts 的结束原因转换为 OpenAI 客户端可识别的值。
func NormalizeFinishReason(value any) string {
	if value == nil {
		return "stop"
	}
	reason, ok := value.(string)
	if !ok || reason == "" || reason == "other" {
		return "stop"
	}
	return reason
}

// ChatCompletion 是聚合后的 OpenAI chat.completion 结构。
type ChatCompletion struct {
	ID      string   `json:"id,omitempty"`
	Object  string   `json:"object"`
	Created int64    `json:"created,omitempty"`
	Model   string   `json:"model,omitempty"`
	Choices []Choice `json:"choices"`
	Usage   any      `json:"usage,omitempty"`
}

// Choice 是补全结果中的单个候选。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Message 是 assistant 消息体。
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall 是模型请求调用的工具。
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 是工具调用的函数名与参数（流式分片会累加）。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ParseUpstream 兼容上游返回普通 JSON 或（错误地）返回 SSE 的情况。
// 与 Python 参考实现一致：JSON 能解析就原样返回（附带 finish_reason 归一化），
// 只有解析失败时才退回按 SSE 聚合。
func ParseUpstream(raw []byte) any {
	var result any
	if err := json.Unmarshal(raw, &result); err == nil {
		if obj, ok := result.(map[string]any); ok {
			if choices, ok := obj["choices"].([]any); ok {
				for _, item := range choices {
					if choice, ok := item.(map[string]any); ok {
						choice["finish_reason"] = NormalizeFinishReason(choice["finish_reason"])
					}
				}
			}
		}
		return result
	}
	return ParseSSEBytes(raw)
}

// ParseSSEBytes 把上游 SSE 聚合为一个 OpenAI chat.completion 结构。
func ParseSSEBytes(raw []byte) any {
	textParts := []string{}
	reasoningParts := []string{}
	toolCalls := map[int]*ToolCall{}
	order := []int{}

	var id string
	var created int64
	var model string
	var usage any
	var lastFinish any

	for _, line := range splitSSELines(raw) {
		payload, ok := sseData(line)
		if !ok || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}

		// CodeArts 可能以 HTTP 200 返回业务错误，需要保留错误信息。
		if code, hasCode := chunk["error_code"]; hasCode && code != nil {
			return errorResponse(code, chunk["error_msg"])
		}
		if msg, hasMsg := chunk["error_msg"]; hasMsg && msg != nil {
			if str, ok := msg.(string); ok && str != "" {
				return errorResponse(chunk["error_code"], str)
			}
		}

		if v, ok := chunk["id"].(string); ok {
			id = v
		}
		if v, ok := chunk["created"].(float64); ok {
			created = int64(v)
		}
		if v, ok := chunk["model"].(string); ok {
			model = v
		}
		if v, ok := chunk["usage"]; ok && v != nil {
			usage = v
		}

		choices, _ := chunk["choices"].([]any)
		for _, item := range choices {
			choice, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if reason, ok := choice["finish_reason"]; ok && reason != nil {
				lastFinish = reason
			}
			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			if content, ok := delta["content"].(string); ok {
				textParts = append(textParts, content)
			}
			if reasoning, ok := delta["reasoning_content"].(string); ok {
				reasoningParts = append(reasoningParts, reasoning)
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, callItem := range calls {
				call, ok := callItem.(map[string]any)
				if !ok {
					continue
				}
				index := 0
				if v, ok := call["index"].(float64); ok {
					index = int(v)
				}
				current, exists := toolCalls[index]
				if !exists {
					current = &ToolCall{Type: "function"}
					toolCalls[index] = current
					order = append(order, index)
				}
				if v, ok := call["id"].(string); ok && v != "" {
					current.ID = v
				}
				if v, ok := call["type"].(string); ok && v != "" {
					current.Type = v
				}
				if fn, ok := call["function"].(map[string]any); ok {
					if v, ok := fn["name"].(string); ok {
						current.Function.Name += v
					}
					if v, ok := fn["arguments"].(string); ok {
						current.Function.Arguments += v
					}
				}
			}
		}
	}

	message := Message{Role: "assistant", Content: strings.Join(textParts, "")}
	if len(reasoningParts) > 0 {
		message.ReasoningContent = strings.Join(reasoningParts, "")
	}
	if len(order) > 0 {
		sort.Ints(order)
		for _, index := range order {
			message.ToolCalls = append(message.ToolCalls, *toolCalls[index])
		}
	}

	finish := NormalizeFinishReason(lastFinish)
	if lastFinish == nil && len(message.ToolCalls) > 0 {
		finish = "tool_calls"
	}

	result := ChatCompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []Choice{{Index: 0, Message: message, FinishReason: finish}},
		Usage:   usage,
	}
	return result
}

func errorResponse(code any, msg any) map[string]any {
	message, _ := msg.(string)
	if message == "" {
		message = "CodeArts upstream error"
	}
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    code,
		},
	}
}

// splitSSELines 按行切分，兼容 \n 与 \r\n。
func splitSSELines(raw []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// sseData 提取 "data:" 后的负载；非 data 行返回 ok=false。
func sseData(line string) ([]byte, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if !strings.HasPrefix(trimmed, "data:") {
		return nil, false
	}
	return []byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))), true
}

// NewChunkID 生成代理补齐帧使用的 ID。
func NewChunkID() string {
	return "proxy-finish"
}

// Now 返回当前 Unix 时间戳。
func Now() int64 {
	return time.Now().Unix()
}
