package openai

import (
	"errors"
	"fmt"
)

// ShapeRequest 把下游 OpenAI Chat Completions 请求整形为 CodeArts 上游认的形态。
//
// 上游 /api/v2/chat/completions 有三处与标准 OpenAI 不一致，不处理时的表现就是
// 「不支持 function call」：
//
//  1. 必须显式带 tool_stream=true，否则模型把工具调用写成普通文本，不会产出
//     结构化的 tool_calls（客户端只看到一段描述调用的文字）。
//  2. tool_stream 仅在 stream=true 时生效，因此带工具的请求必须走流式；
//     非流式客户端由调用方把上游 SSE 聚合回单个 JSON。
//  3. tool_choice 只接受字符串（auto/none/required），对象形态
//     {"type":"function",...} 会被上游以 InferHub.001001005.400 拒绝。
//     指定具体函数时改为「只下发该函数 + required」，语义等价。
//
// 另外上游对工具历史很挑：assistant.tool_calls 必须与 role=tool 的结果完整成对，
// 孤儿调用或残缺结果会让整轮请求 400，并让后续每轮都失败，因此这里会丢掉不完整
// 的历史片段。
func ShapeRequest(req map[string]any) error {
	shapeMessages(req)

	// tool_stream 只在流式下生效，非流式请求里带上也无副作用。
	req["tool_stream"] = true

	tools, _ := req["tools"].([]any)
	if len(tools) == 0 {
		return nil
	}

	choice, err := parseToolChoice(req["tool_choice"])
	if err != nil {
		return err
	}

	switch choice.Mode {
	case "function":
		selected, err := selectTool(tools, choice.Name)
		if err != nil {
			return err
		}
		req["tools"] = selected
		req["tool_choice"] = "required"
	case "", "auto":
		req["tool_choice"] = "auto"
	default:
		req["tool_choice"] = choice.Mode
	}

	// 带工具时必须流式：上游只在流式下产出 tool_calls。
	req["stream"] = true
	return nil
}

// toolChoice 是归一化后的 tool_choice。
type toolChoice struct {
	// Mode 取 "" / "auto" / "none" / "required" / "function"。
	Mode string
	// Name 仅在 Mode=="function" 时有效。
	Name string
}

// parseToolChoice 归一化 tool_choice：字符串或 {"type":"function","function":{...}}。
func parseToolChoice(value any) (toolChoice, error) {
	switch v := value.(type) {
	case nil:
		return toolChoice{}, nil
	case string:
		switch v {
		case "auto", "none", "required":
			return toolChoice{Mode: v}, nil
		}
		// 未知取值按 auto 处理，避免因为客户端扩展枚举直接 400。
		return toolChoice{Mode: "auto"}, nil
	case map[string]any:
		fn, _ := v["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			return toolChoice{}, errors.New(`tool_choice.function.name 不能为空`)
		}
		return toolChoice{Mode: "function", Name: name}, nil
	}
	return toolChoice{Mode: "auto"}, nil
}

// selectTool 只保留指定名字的工具，配合 required 复现「强制调用某个函数」。
func selectTool(tools []any, name string) ([]any, error) {
	var selected []any
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil || fn["name"] != name {
			continue
		}
		selected = append(selected, tool)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("tool_choice 指定的函数 %q 不在 tools 中", name)
	}
	if len(selected) > 1 {
		return nil, fmt.Errorf("tool_choice 指定的函数 %q 在 tools 中重复定义", name)
	}
	return selected, nil
}

// shapeMessages 规整消息历史里的工具相关字段。
func shapeMessages(req map[string]any) {
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}

	// 先收集已有的工具结果，用于判断调用是否完整成对。
	resultIDs := map[string]struct{}{}
	for _, item := range msgs {
		msg, _ := item.(map[string]any)
		if msg == nil || msg["role"] != "tool" {
			continue
		}
		if id, _ := msg["tool_call_id"].(string); id != "" {
			resultIDs[id] = struct{}{}
		}
	}

	// 再把 assistant 的 tool_calls 分成「完整成对」与「残缺」两拨：
	// 残缺的调用整组丢掉（上游会因为一个孤儿调用让整轮 400）。
	drop := make([]bool, len(msgs))
	surviving := map[string]struct{}{}
	for i, item := range msgs {
		msg, ok := item.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		calls, _ := msg["tool_calls"].([]any)
		if len(calls) == 0 {
			continue
		}
		if !callsPaired(calls, resultIDs) {
			delete(msg, "tool_calls")
			if isEmptyContent(msg["content"]) && msg["reasoning_content"] == nil {
				// 去掉调用后这条消息没有任何内容，一并丢掉。
				drop[i] = true
			}
			continue
		}
		for _, item := range calls {
			if call, ok := item.(map[string]any); ok {
				if id, _ := call["id"].(string); id != "" {
					surviving[id] = struct{}{}
				}
			}
		}
	}

	// 最后按保留结果重建消息列表，并规整 content 形态。
	shaped := make([]any, 0, len(msgs))
	for i, item := range msgs {
		if drop[i] {
			continue
		}
		msg, ok := item.(map[string]any)
		if !ok {
			shaped = append(shaped, item)
			continue
		}
		switch msg["role"] {
		case "assistant":
			// 带 tool_calls 的 assistant 消息，content 必须是 null 而非空串。
			if calls, _ := msg["tool_calls"].([]any); len(calls) > 0 && isEmptyContent(msg["content"]) {
				msg["content"] = nil
			}
			shaped = append(shaped, msg)
		case "tool":
			id, _ := msg["tool_call_id"].(string)
			if _, ok := surviving[id]; !ok {
				// 调用已被丢掉（或本来就不存在）的结果同样会让上游报错。
				continue
			}
			if isEmptyContent(msg["content"]) {
				msg["content"] = "(no output)"
			}
			shaped = append(shaped, msg)
		default:
			shaped = append(shaped, msg)
		}
	}
	req["messages"] = shaped
}

// callsPaired 判断一组 tool_calls 是否每个都有对应的 tool 结果。
func callsPaired(calls []any, resultIDs map[string]struct{}) bool {
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			return false
		}
		id, _ := call["id"].(string)
		if id == "" {
			return false
		}
		if _, ok := resultIDs[id]; !ok {
			return false
		}
	}
	return true
}

// isEmptyContent 判断 content 是否为空（null、空串或空分片数组）。
func isEmptyContent(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	}
	return false
}
