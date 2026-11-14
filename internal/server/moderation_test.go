package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

)

// moderationBody 是上游内容审核命中的典型响应体（HTTP 403）。
const moderationBody = `{"error_code":"InferHub.ModelArts.81011.403",` +
	`"error_msg":"抱歉，我无法提供关于您提问内容的建议。作为 AI 编程助手，我更擅长协助您处理代码编写、代码调试、架构设计或技术实现。"}`

// TestChatContentModerationReturns403 验证非流式下审核命中不再被包成 502。
//
// 包成 502 会让客户端以为网关故障并重试，而审核是内容层面的问题，重试无效。
func TestChatContentModerationReturns403(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, moderationBody)
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "openpangu-2.0-pro")
	srv.chatURL = fake.URL

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != "content_filter" || payload.Error.Code != "content_filter" {
		t.Errorf("错误类型应为 content_filter: %+v", payload.Error)
	}
	if !strings.Contains(payload.Error.Message, "无法提供关于您提问内容") {
		t.Errorf("应透传上游原文, 得到 %q", payload.Error.Message)
	}
}

// TestChatContentModerationInStreamReturns403 验证 HTTP 200 + 首帧审核错误
// 不会先写出 200 响应头，而是整体按 403 处理。
func TestChatContentModerationInStreamReturns403(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			"data: {\"text\":\"[DONE]\",\"error_code\":\"InferHub.ModelArts.81011.403\",\"error_msg\":\"敏感内容\"}\n\n")
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "GLM-5.2")
	srv.chatURL = fake.URL

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"GLM-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "data:") {
		t.Errorf("审核错误不应以 SSE 帧形式透传: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content_filter"`) {
		t.Errorf("错误码应为 content_filter: %s", rec.Body.String())
	}
}

// TestChatNonStreamEmbeddedModerationReturns403 验证非流式请求拿到 SSE 形态的
// 审核错误时同样映射成 403（而不是把错误对象当正常结果返回）。
func TestChatNonStreamEmbeddedModerationReturns403(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			"data: {\"text\":\"[DONE]\",\"error_code\":\"InferHub.ModelArts.81011.403\",\"error_msg\":\"敏感内容\"}\n\n")
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "openpangu-2.0-pro")
	srv.chatURL = fake.URL

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

// TestChatSendsIDEHeaders 验证补齐的官方客户端头与未签名的会话头。
func TestChatSendsIDEHeaders(t *testing.T) {
	var seen http.Header
	runChatTestWithCapture(t, "openpangu-2.0-pro", false,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		"application/json", &seen)

	cases := map[string]string{
		"app-id":          "CodeAgent3.0",
		"is_confidential": "false",
		"x-language":      "zh-cn",
		"accept":          "text/event-stream",
	}
	for name, want := range cases {
		if got := seen.Get(name); got != want {
			t.Errorf("头 %s = %q, want %q", name, got, want)
		}
	}
	if seen.Get("x-snap-traceid") == "" {
		t.Error("缺少 x-snap-traceid")
	}
	if id := seen.Get("Chat-Id"); len(id) != 32 {
		t.Errorf("Chat-Id 应为 32 位十六进制, 得到 %q", id)
	}
	if seen.Get("Session-Id") == "" {
		t.Error("缺少 Session-Id")
	}

	signed := seen.Get("Authorization")
	for _, name := range []string{"app-id", "is_confidential", "x-language", "x-snap-traceid"} {
		if !strings.Contains(signed, name) {
			t.Errorf("签名头应包含 %s: %s", name, signed)
		}
	}
	// 会话头在签名之后追加，不应进入 SignedHeaders。
	if strings.Contains(signed, "chat-id") || strings.Contains(signed, "session-id") {
		t.Errorf("会话头不应参与签名: %s", signed)
	}
}

// TestChatBodyCarriesChatID 验证 body 带 chat_id / prompt_cache_key 且同会话稳定。
func TestChatBodyCarriesChatID(t *testing.T) {
	var bodies []map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "openpangu-2.0-pro")
	srv.chatURL = fake.URL

	request := func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"看下目录"}]}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	request()
	request()

	if len(bodies) != 2 {
		t.Fatalf("上游调用次数 = %d", len(bodies))
	}
	id, _ := bodies[0]["chat_id"].(string)
	if len(id) != 32 {
		t.Fatalf("chat_id 应为 32 位十六进制, 得到 %v", bodies[0]["chat_id"])
	}
	if bodies[0]["prompt_cache_key"] != id {
		t.Errorf("prompt_cache_key 应与 chat_id 一致: %v", bodies[0]["prompt_cache_key"])
	}
	if bodies[1]["chat_id"] != id {
		t.Errorf("同一会话的 chat_id 应稳定: %v != %v", bodies[1]["chat_id"], id)
	}
	if _, leaked := bodies[0]["conversation_id"]; leaked {
		t.Error("conversation_id 是下游字段，不应带给上游")
	}
}

// TestChatTrimRetryOnModeration 验证开启降级重试后，命中审核会裁掉早期历史重试一次。
func TestChatTrimRetryOnModeration(t *testing.T) {
	var bodies []map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)

		if len(bodies) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, moderationBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "openpangu-2.0-pro")
	srv.chatURL = fake.URL
	srv.trimRetry = 2

	body := `{"model":"openpangu-2.0-pro","messages":[` +
		`{"role":"system","content":"你是编程助手"},` +
		`{"role":"user","content":"第一轮提问"},` +
		`{"role":"assistant","content":"第一轮回答"},` +
		`{"role":"user","content":"第二轮提问"}]}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("重试后应成功, status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(bodies) != 2 {
		t.Fatalf("上游调用次数 = %d, want 2", len(bodies))
	}

	first, _ := bodies[0]["messages"].([]any)
	second, _ := bodies[1]["messages"].([]any)
	if len(first) != 4 {
		t.Fatalf("首次请求应带完整历史, 得到 %d 条", len(first))
	}
	if len(second) != 3 {
		t.Fatalf("重试应保留 system + 尾部 2 条, 得到 %d 条", len(second))
	}
	if system, _ := second[0].(map[string]any); system["role"] != "system" {
		t.Errorf("重试仍需保留 system 提示: %v", second[0])
	}
	if bodies[1]["chat_id"] != bodies[0]["chat_id"] {
		t.Error("重试应复用同一 chat_id")
	}
}

// TestChatNoTrimRetryWhenDisabled 验证默认配置下不重试。
func TestChatNoTrimRetryWhenDisabled(t *testing.T) {
	var calls int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, moderationBody)
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "openpangu-2.0-pro")
	srv.chatURL = fake.URL

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if calls != 1 {
		t.Errorf("未开启降级重试时不应重复调用, 次数 = %d", calls)
	}
}

// TestPreflightStreamReplaysWholeBody 验证未命中错误时前置探测不丢字节。
func TestPreflightStreamReplaysWholeBody(t *testing.T) {
	const stream = ": ping\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	reader, fail := preflightStream(io.NopCloser(strings.NewReader(stream)))
	if fail != nil {
		t.Fatalf("不应判定为错误: %+v", fail)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != stream {
		t.Errorf("重放内容与原始流不一致:\n%q\n%q", out, stream)
	}
}

// TestPreflightStreamDetectsError 验证首帧错误被识别为审核失败。
func TestPreflightStreamDetectsError(t *testing.T) {
	const stream = "data: {\"text\":\"[DONE]\",\"error_code\":\"InferHub.ModelArts.81011.403\",\"error_msg\":\"敏感内容\"}\n\n"
	_, fail := preflightStream(io.NopCloser(strings.NewReader(stream)))
	if fail == nil {
		t.Fatal("应判定为审核失败")
	}
	if fail.Status != http.StatusForbidden || fail.Code != "content_filter" {
		t.Errorf("错误分类有误: %+v", fail)
	}
}

// TestPreflightStreamIgnoresNonModerationChunk 验证正常首帧不会被当成错误。
func TestPreflightStreamIgnoresNonModerationChunk(t *testing.T) {
	const stream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	reader, fail := preflightStream(io.NopCloser(strings.NewReader(stream)))
	if fail != nil {
		t.Fatalf("正常首帧不应判定为错误: %+v", fail)
	}
	out, _ := io.ReadAll(reader)
	if string(out) != stream {
		t.Errorf("重放内容不一致: %q", out)
	}
}
