package openai

import (
	"encoding/json"
	"testing"
)

func TestNormalizeFinishReason(t *testing.T) {
	cases := []struct {
		input any
		want  string
	}{
		{nil, "stop"},
		{"", "stop"},
		{"other", "stop"},
		{"stop", "stop"},
		{"length", "length"},
		{"tool_calls", "tool_calls"},
		{123, "stop"},
	}
	for _, tc := range cases {
		if got := NormalizeFinishReason(tc.input); got != tc.want {
			t.Errorf("NormalizeFinishReason(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseUpstreamPlainJSON(t *testing.T) {
	raw := []byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"other"}]}`)
	result := ParseUpstream(raw)
	obj, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("期望 map, 得到 %T", result)
	}
	choices := obj["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason 应归一化为 stop, 得到 %v", choice["finish_reason"])
	}
}

func TestParseUpstreamSSE(t *testing.T) {
	raw := []byte(
		"data: {\"id\":\"chunk-1\",\"created\":100,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"other\"}]}\n\n" +
			"data: [DONE]\n\n",
	)
	result := ParseUpstream(raw)
	completion, ok := result.(ChatCompletion)
	if !ok {
		t.Fatalf("期望 ChatCompletion, 得到 %T", result)
	}
	if completion.ID != "chunk-1" || completion.Created != 100 || completion.Model != "m" {
		t.Errorf("元数据聚合错误: %+v", completion)
	}
	if len(completion.Choices) != 1 {
		t.Fatalf("choices 数量 = %d", len(completion.Choices))
	}
	if completion.Choices[0].Message.Content != "你好" {
		t.Errorf("content = %q, want 你好", completion.Choices[0].Message.Content)
	}
	if completion.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", completion.Choices[0].FinishReason)
	}
}

func TestParseSSEBytesAggregatesReasoning(t *testing.T) {
	raw := []byte(
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"想\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"一下\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"答\"}}]}\n\n",
	)
	result := ParseSSEBytes(raw)
	completion := result.(ChatCompletion)
	message := completion.Choices[0].Message
	if message.ReasoningContent != "想一下" {
		t.Errorf("reasoning_content = %q", message.ReasoningContent)
	}
	if message.Content != "答" {
		t.Errorf("content = %q", message.Content)
	}
}

func TestParseSSEBytesAggregatesToolCalls(t *testing.T) {
	raw := []byte(
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_\",\"arguments\":\"{\\\"a\\\":\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"weather\",\"arguments\":\"1}\"}}]}}]}\n\n",
	)
	result := ParseSSEBytes(raw)
	completion := result.(ChatCompletion)
	choice := completion.Choices[0]
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls 数量 = %d", len(choice.Message.ToolCalls))
	}
	call := choice.Message.ToolCalls[0]
	if call.Function.Name != "get_weather" {
		t.Errorf("函数名分片未累加: %q", call.Function.Name)
	}
	if call.Function.Arguments != `{"a":1}` {
		t.Errorf("参数分片未累加: %q", call.Function.Arguments)
	}
	if choice.FinishReason != "tool_calls" {
		t.Errorf("有 tool_calls 且无 finish_reason 时应为 tool_calls, 得到 %q", choice.FinishReason)
	}
}

func TestParseSSEBytesKeepsMultipleToolCallOrder(t *testing.T) {
	raw := []byte(
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"b\",\"function\":{\"name\":\"second\"}},{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"first\"}}]}}]}\n\n",
	)
	result := ParseSSEBytes(raw)
	completion := result.(ChatCompletion)
	calls := completion.Choices[0].Message.ToolCalls
	if len(calls) != 2 || calls[0].Function.Name != "first" || calls[1].Function.Name != "second" {
		t.Errorf("tool_calls 应按 index 排序: %+v", calls)
	}
}

func TestParseSSEBytesSurfacesUpstreamError(t *testing.T) {
	raw := []byte("data: {\"error_code\":\"SNAP.0001\",\"error_msg\":\"model is not registered\"}\n\n")
	result := ParseSSEBytes(raw)
	obj, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("期望 map, 得到 %T", result)
	}
	errObj, ok := obj["error"].(map[string]any)
	if !ok {
		t.Fatalf("期望 error 字段: %+v", obj)
	}
	if errObj["message"] != "model is not registered" {
		t.Errorf("错误信息未透传: %v", errObj["message"])
	}
	if errObj["type"] != "upstream_error" {
		t.Errorf("错误类型 = %v", errObj["type"])
	}
}

func TestParseSSEBytesIgnoresMalformedLines(t *testing.T) {
	raw := []byte(
		"data: {not json}\n\n" +
			": keep-alive comment\n\n" +
			"event: ping\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n",
	)
	result := ParseSSEBytes(raw)
	completion := result.(ChatCompletion)
	if completion.Choices[0].Message.Content != "ok" {
		t.Errorf("content = %q, want ok", completion.Choices[0].Message.Content)
	}
}

func TestParseUpstreamEmptyFallsBackToSSE(t *testing.T) {
	// 空响应既不是合法 JSON 也不是合法 SSE，应得到空聚合结果而非 panic。
	result := ParseUpstream([]byte(""))
	completion, ok := result.(ChatCompletion)
	if !ok {
		t.Fatalf("期望 ChatCompletion, 得到 %T", result)
	}
	if completion.Object != "chat.completion" {
		t.Errorf("object = %q", completion.Object)
	}
	if completion.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", completion.Choices[0].FinishReason)
	}
}

func TestChatCompletionJSONShape(t *testing.T) {
	completion := ChatCompletion{
		Object: "chat.completion",
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: "hi"},
			FinishReason: "stop",
		}},
	}
	encoded, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["object"] != "chat.completion" {
		t.Errorf("object 字段缺失: %s", encoded)
	}
	if _, exists := decoded["id"]; exists {
		t.Errorf("空 ID 应被省略: %s", encoded)
	}
}
