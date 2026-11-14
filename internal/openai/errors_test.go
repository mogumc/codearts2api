package openai

import "testing"

func TestIsContentModeration(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		message string
		want    bool
	}{
		{
			name:    "InferHub 包装后的 81011",
			code:    "InferHub.ModelArts.81011.403",
			message: "内容未通过审核",
			want:    true,
		},
		{
			name:    "裸 ModelArts.81011",
			code:    "ModelArts.81011",
			message: "May contain sensitive information, please try again.",
			want:    true,
		},
		{
			name:    "CodeArts 自定义话术",
			code:    "",
			message: "抱歉，我无法提供关于您提问内容的建议。作为 AI 编程助手，我更擅长协助您处理代码编写。",
			want:    true,
		},
		{
			name:    "只有英文文案",
			code:    "",
			message: "May contain SENSITIVE INFORMATION",
			want:    true,
		},
		{
			name:    "模型未注册不是审核",
			code:    "InferHub.002002009.404",
			message: "model is not registered",
			want:    false,
		},
		{
			name:    "参数错误不是审核",
			code:    "InferHub.001001005.400",
			message: "The request param is invalid",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsContentModeration(tc.code, tc.message); got != tc.want {
				t.Errorf("IsContentModeration(%q, %q) = %v, want %v", tc.code, tc.message, got, tc.want)
			}
		})
	}
}

func TestExtractUpstreamError(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantCode    string
		wantMessage string
	}{
		{
			name:        "网关层错误",
			raw:         `{"error_code":"InferHub.ModelArts.81011.403","error_msg":"敏感内容"}`,
			wantCode:    "InferHub.ModelArts.81011.403",
			wantMessage: "敏感内容",
		},
		{
			name:        "OpenAI 层错误",
			raw:         `{"error":{"message":"bad request","code":"invalid_request"}}`,
			wantCode:    "invalid_request",
			wantMessage: "bad request",
		},
		{
			name:        "非 JSON 原文兜底",
			raw:         `upstream gateway timeout`,
			wantCode:    "",
			wantMessage: "upstream gateway timeout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, message := ExtractUpstreamError([]byte(tc.raw))
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if message != tc.wantMessage {
				t.Errorf("message = %q, want %q", message, tc.wantMessage)
			}
		})
	}
}

// TestExtractEmbeddedErrorPrefersErrorCode 验证错误帧先看 error_code 再看 text。
//
// 上游的审核错误帧同时带 "text":"[DONE]"，若先按 text 判断结束就会漏掉错误。
func TestExtractEmbeddedErrorPrefersErrorCode(t *testing.T) {
	chunk := map[string]any{
		"text":       "[DONE]",
		"error_code": "InferHub.ModelArts.81011.403",
		"error_msg":  "抱歉，我无法提供关于您提问内容的建议。",
	}
	code, message, found := ExtractEmbeddedError(chunk)
	if !found {
		t.Fatal("应识别出内嵌错误")
	}
	if code != "InferHub.ModelArts.81011.403" {
		t.Errorf("code = %q", code)
	}
	if message == "" {
		t.Error("message 不应为空")
	}
}

func TestExtractEmbeddedErrorIgnoresSuccess(t *testing.T) {
	cases := []map[string]any{
		{"text": "[DONE]", "error_code": "0"},
		{"choices": []any{}},
		{},
	}
	for _, chunk := range cases {
		if _, _, found := ExtractEmbeddedError(chunk); found {
			t.Errorf("不应识别为错误: %v", chunk)
		}
	}
}
