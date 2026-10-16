// Command testclient 是本地联调脚本：通过代理调用 CodeArts 模型。
//
// 用法（先启动代理）：
//
//	go run ./cmd/testclient
//	go run ./cmd/testclient glm-5.3-flash
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
	model := "openpangu-2.0-pro"
	if len(os.Args) > 1 {
		model = os.Args[1]
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

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
