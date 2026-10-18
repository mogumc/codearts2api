// Command testclient 是本地联调脚本：通过代理调用 CodeArts 模型。
//
// 用法（先启动代理）：
//
//	go run ./cmd/testclient                  # 模型列表 + 普通对话
//	go run ./cmd/testclient glm-5.3-flash    # 指定模型
//	go run ./cmd/testclient tools            # 工具调用（流式 + 非流式）
//	go run ./cmd/testclient tools glm-5.3-flash
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const baseURL = "http://127.0.0.1:8787/v1"

func main() {
	args := os.Args[1:]
	model := "openpangu-2.0-pro"
	wantTools := false
	for _, arg := range args {
		if arg == "tools" {
			wantTools = true
			continue
		}
		model = arg
	}

	if wantTools {
		if err := testTools(model); err != nil {
			fmt.Println("失败:", err)
			os.Exit(1)
		}
		return
	}

	if err := testModels(); err != nil {
		fmt.Println("失败:", err)
		os.Exit(1)
	}
	if err := testChat(model); err != nil {
		fmt.Println("失败:", err)
		os.Exit(1)
	}
}

func testModels() error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(baseURL + "/models")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	pretty, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Println("== GET /models ==")
	fmt.Println(string(pretty))
	return nil
}

func testChat(model string) error {
	payload := map[string]any{
		"model":      model,
		"messages":   []any{map[string]any{"role": "user", "content": "只回复：成功"}},
		"stream":     false,
		"max_tokens": 64,
	}
	encoded, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Post(baseURL+"/chat/completions", "application/json", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	pretty, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println("\n== POST /chat/completions ==")
	fmt.Println(truncate(string(pretty), 2000))
	return nil
}

// testTools 发一轮工具调用请求，验证代理是否把 tool_calls 完整带回。
//
// 非流式与流式各跑一次：非流式看 message.tool_calls，流式看 delta.tool_calls。
func testTools(model string) error {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "查询指定城市的天气",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string", "description": "城市名"}},
				"required":   []string{"city"},
			},
		},
	}}

	for _, stream := range []bool{false, true} {
		payload := map[string]any{
			"model":    model,
			"messages": []any{map[string]any{"role": "user", "content": "南京今天天气怎么样？"}},
			"stream":   stream,
			"tools":    tools,
			// 对象形态：代理会转成「只下发该函数 + required」的字符串形态。
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
		}
		encoded, _ := json.Marshal(payload)

		client := &http.Client{Timeout: 180 * time.Second}
		resp, err := client.Post(baseURL+"/chat/completions", "application/json", bytes.NewReader(encoded))
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		label := "非流式"
		if stream {
			label = "流式"
		}
		fmt.Printf("\n== tools（%s, model=%s, HTTP %d）==\n", label, model, resp.StatusCode)
		fmt.Println(truncate(string(body), 2000))

		if !bytes.Contains(body, []byte("tool_calls")) {
			fmt.Println(">>> 没有 tool_calls：上游仍未进入工具调用模式，请贴上面这段给我。")
			continue
		}
		fmt.Println(">>> 已拿到 tool_calls，工具调用链路正常。")
	}
	return nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
