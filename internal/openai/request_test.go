package openai

import (
	"encoding/json"
	"testing"
)

// decodeRequest 把一个 JSON 请求体解成整形函数使用的 map 形态。
func decodeRequest(t *testing.T, raw string) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("解析请求失败: %v", err)
	}
	return req
}

const weatherTool = `{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}`

const stockTool = `{"type":"function","function":{"name":"get_stock","parameters":{"type":"object","properties":{"code":{"type":"string"}}}}}`

// TestShapeRequestEnablesToolStream 验证带工具的请求会开启 tool_stream 并强制流式。
func TestShapeRequestEnablesToolStream(t *testing.T) {
	req := decodeRequest(t, `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}],"tools":[`+weatherTool+`],"tool_choice":"auto"}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	if req["tool_stream"] != true {
		t.Errorf("tool_stream = %v, want true", req["tool_stream"])
	}
	if req["stream"] != true {
		t.Errorf("带工具时必须强制流式, stream = %v", req["stream"])
	}
	if req["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
	}
}

// TestShapeRequestDefaultsToolChoice 验证未指定 tool_choice 时补 auto。
func TestShapeRequestDefaultsToolChoice(t *testing.T) {
	req := decodeRequest(t, `{"messages":[{"role":"user","content":"hi"}],"tools":[`+weatherTool+`]}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	if req["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
	}
}

// TestShapeRequestKeepsToolsWithoutStream 验证不带工具的请求不改变 stream。
func TestShapeRequestKeepsToolsWithoutStream(t *testing.T) {
	req := decodeRequest(t, `{"stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	if req["stream"] != false {
		t.Errorf("无工具时不应强制流式, stream = %v", req["stream"])
	}
	if req["tool_stream"] != true {
		t.Errorf("tool_stream 应恒为 true, 得到 %v", req["tool_stream"])
	}
}

// TestShapeRequestConvertsObjectToolChoice 验证对象形态 tool_choice 被改为
// 「只下发该函数 + required」字符串形态（上游只认字符串）。
func TestShapeRequestConvertsObjectToolChoice(t *testing.T) {
	req := decodeRequest(t, `{"messages":[{"role":"user","content":"hi"}],"tools":[`+weatherTool+`,`+stockTool+`],`+
		`"tool_choice":{"type":"function","function":{"name":"get_stock"}}}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	if req["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want required", req["tool_choice"])
	}
	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools 应被收窄为 1 个, 得到 %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	if fn["name"] != "get_stock" {
		t.Errorf("保留的工具 = %v", fn["name"])
	}
}

// TestShapeRequestRejectsUnknownToolChoiceFunction 验证指定了不存在的函数时报错。
func TestShapeRequestRejectsUnknownToolChoiceFunction(t *testing.T) {
	req := decodeRequest(t, `{"messages":[{"role":"user","content":"hi"}],"tools":[`+weatherTool+`],`+
		`"tool_choice":{"type":"function","function":{"name":"nope"}}}`)

	if err := ShapeRequest(req); err == nil {
		t.Error("指定不存在的函数应返回错误")
	}
}

// TestShapeRequestDropsUnpairedToolHistory 验证残缺的调用/结果对被剥离，
// 完整的一对保留。
func TestShapeRequestDropsUnpairedToolHistory(t *testing.T) {
	req := decodeRequest(t, `{"messages":[
		{"role":"user","content":"查天气"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_ok","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_ok","content":"晴"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_orphan","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_missing","content":"孤立的工具结果"}
	]}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应保留 3 条消息（user + 完整的一对）, 得到 %d", len(msgs))
	}

	assistant := msgs[1].(map[string]any)
	if len(assistant["tool_calls"].([]any)) != 1 {
		t.Errorf("完整的调用应保留: %v", assistant["tool_calls"])
	}
	if assistant["content"] != nil {
		t.Errorf("带 tool_calls 的 assistant 消息 content 应为 null, 得到 %v", assistant["content"])
	}

	tool := msgs[2].(map[string]any)
	if tool["content"] != "晴" {
		t.Errorf("工具结果内容被改写: %v", tool["content"])
	}
}

// TestShapeRequestDropsPartiallyPairedCalls 验证一组调用里只要有一个缺结果，
// 整组调用连同它的结果都被丢掉（不能留下孤儿 tool 消息）。
func TestShapeRequestDropsPartiallyPairedCalls(t *testing.T) {
	req := decodeRequest(t, `{"messages":[
		{"role":"user","content":"查天气和股价"},
		{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{}"}},
			{"id":"call_b","type":"function","function":{"name":"get_stock","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"call_a","content":"晴"}
	]}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("残缺的一组调用应该整组丢掉, 剩 %d 条: %v", len(msgs), msgs)
	}
	if role := msgs[0].(map[string]any)["role"]; role != "user" {
		t.Errorf("剩下的是 %v, 期望 user", role)
	}
}

// TestShapeRequestFillsEmptyToolContent 验证空的工具结果被补成占位文本。
func TestShapeRequestFillsEmptyToolContent(t *testing.T) {
	req := decodeRequest(t, `{"messages":[
		{"role":"user","content":"跑个命令"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":""}
	]}`)

	if err := ShapeRequest(req); err != nil {
		t.Fatalf("整形失败: %v", err)
	}
	msgs, _ := req["messages"].([]any)
	tool := msgs[2].(map[string]any)
	if tool["content"] != "(no output)" {
		t.Errorf("空工具结果应补占位文本, 得到 %v", tool["content"])
	}
}
