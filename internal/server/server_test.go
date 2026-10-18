package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codearts2api/internal/catalog"
	"codearts2api/internal/config"
	"codearts2api/internal/signer"
	"codearts2api/internal/upstream"
)

// newTestCatalog 构造一个只含指定模型的目录，避免测试触网。
func newTestCatalog(t *testing.T, models []catalog.Model) *catalog.Catalog {
	t.Helper()
	dir := t.TempDir()
	endpoints := config.DefaultEndpoints()
	creds := signer.New("demo-ak", "demo-sk")
	cat := catalog.New(
		upstream.New(creds, endpoints.CodeArtsHost()),
		upstream.New(creds, endpoints.OpenGWHost()),
		dir+"/models-cache.json",
		endpoints,
	)
	cat.SaveCache(models)
	if !cat.LoadCache() {
		t.Fatal("测试模型目录装载失败")
	}
	return cat
}

func agentModel(id string) catalog.Model {
	return catalog.Model{ID: id, Object: "model", OwnedBy: "codearts", Channel: catalog.ChannelAgent, Name: id}
}

func benefitModel(id string) catalog.Model {
	return catalog.Model{ID: id, Object: "model", OwnedBy: "codearts", Channel: catalog.ChannelBenefit, Name: id}
}

func newTestServer(t *testing.T, models []catalog.Model) *Server {
	t.Helper()
	cfg := &config.Config{AK: "demo-ak", SK: "demo-sk", ListenHost: "127.0.0.1", ListenPort: 8787}
	client := upstream.New(signer.New("demo-ak", "demo-sk"), config.DefaultEndpoints().CodeArtsHost())
	return New(cfg, newTestCatalog(t, models), client)
}

// newTestServerFor 按指定通道构造只含单个模型的服务。
func newTestServerFor(t *testing.T, id, channel string) *Server {
	t.Helper()
	model := agentModel(id)
	if channel == catalog.ChannelBenefit {
		model = benefitModel(id)
	}
	return newTestServer(t, []catalog.Model{model})
}

func TestListModels(t *testing.T) {
	srv := newTestServer(t, []catalog.Model{agentModel("openpangu-2.0-pro"), benefitModel("glm-5.3-flash")})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("缺少 CORS 头")
	}

	var payload struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Object != "list" || len(payload.Data) != 2 {
		t.Fatalf("响应结构异常: %s", rec.Body.String())
	}
	first := payload.Data[0]
	if first["id"] != "openpangu-2.0-pro" {
		t.Errorf("首个模型 = %v", first["id"])
	}
	if first["owned_by"] != "codearts" {
		t.Errorf("owned_by = %v", first["owned_by"])
	}
	if first["supports_function_calling"] != true {
		t.Errorf("supports_function_calling 应为 true")
	}
	if first["context_window"] != nil {
		t.Errorf("未设置 context_window 时不应输出该字段")
	}
}

func TestGetModelDetail(t *testing.T) {
	ctxWindow := int64(524288)
	model := agentModel("openpangu-2.0-pro")
	model.ContextWindow = &ctxWindow
	srv := newTestServer(t, []catalog.Model{model})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/openpangu-2.0-pro", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["context_window"] != float64(524288) {
		t.Errorf("context_window = %v", payload["context_window"])
	}
	if payload["context_length"] != float64(524288) {
		t.Errorf("context_length = %v", payload["context_length"])
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未知模型应返回 404, 得到 %d", rec.Code)
	}
}

func TestChatCompletionsRejectsUnknownModel(t *testing.T) {
	srv := newTestServer(t, []catalog.Model{agentModel("openpangu-2.0-pro")})

	body := `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != "model_not_found" {
		t.Errorf("错误码 = %v", errObj["code"])
	}
}

func TestChatCompletionsRejectsInvalidJSON(t *testing.T) {
	srv := newTestServer(t, []catalog.Model{agentModel("openpangu-2.0-pro")})

	for _, body := range []string{"", "{not json}"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q 应返回 400, 得到 %d", body, rec.Code)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := newTestServer(t, []catalog.Model{agentModel("openpangu-2.0-pro")})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("预检 status = %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("预检缺少 Allow-Methods")
	}
}

// TestAutoClaimTriggeredOnRequests 验证自动签到钩子在正常请求上被触发。
func TestAutoClaimTriggeredOnRequests(t *testing.T) {
	var calls int
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAutoClaim(func() { calls++ })

	// 模型列表、单模型详情、聊天补全三个入口都应触发。
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/models", nil),
		httptest.NewRequest(http.MethodGet, "/v1/models/openpangu-2.0-pro", nil),
	} {
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 2 {
		t.Errorf("GET 请求触发次数 = %d, 期望 2", calls)
	}

	// 聊天补全：用假上游让请求走通。
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer fake.Close()
	srv.chatURL = fake.URL

	body := `{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"hi"}]}`
	srv.Handler().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if calls != 3 {
		t.Errorf("聊天请求后触发次数 = %d, 期望 3", calls)
	}
}

// TestAutoClaimNotTriggeredOnInvalidRequest 验证畸形请求不触发签到。
func TestAutoClaimNotTriggeredOnInvalidRequest(t *testing.T) {
	var calls int
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAutoClaim(func() { calls++ })

	// 非法 JSON 与未知模型都应在签到之前被拒绝。
	srv.Handler().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{not json}")))
	srv.Handler().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"no-such-model","messages":[]}`)))

	if calls != 0 {
		t.Errorf("畸形请求不应触发签到, 触发次数 = %d", calls)
	}
}

// TestAutoClaimNotTriggeredOnMethodNotAllowed 验证错误方法不触发签到。
func TestAutoClaimNotTriggeredOnMethodNotAllowed(t *testing.T) {
	var calls int
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAutoClaim(func() { calls++ })

	srv.Handler().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/models", nil))

	if calls != 0 {
		t.Errorf("错误方法不应触发签到, 触发次数 = %d", calls)
	}
}

// TestNoAutoClaimHookIsSafe 验证未注入钩子时不会 panic。
func TestNoAutoClaimHookIsSafe(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	// 不调用 SetAutoClaim，钩子为 nil。

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// TestStreamBenefitInjectsFinishReason 验证 benefit 流在缺少 finish_reason 时被补帧。
func TestStreamBenefitInjectsFinishReason(t *testing.T) {
	upstreamSSE := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":null,\"reasoning_content\":\"想\"}}]}\n\n" +
		"data: [DONE]\n\n"

	out := runStreamTest(t, upstreamSSE, "glm-5.3-flash", true)

	if !strings.Contains(out, `"content":""`) {
		t.Errorf("content:null 应被规范化为空字符串:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("benefit 流缺少 finish_reason 时应补帧:\n%s", out)
	}
	// 补帧必须在 [DONE] 之前。
	finishIdx := strings.Index(out, `"finish_reason":"stop"`)
	doneIdx := strings.Index(out, "data: [DONE]")
	if finishIdx < 0 || doneIdx < 0 || finishIdx > doneIdx {
		t.Errorf("补帧应出现在 [DONE] 之前:\n%s", out)
	}
}

// TestStreamBenefitKeepsUpstreamFinishReason 验证上游已有 finish_reason 时不重复补帧。
func TestStreamBenefitKeepsUpstreamFinishReason(t *testing.T) {
	upstreamSSE := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"

	out := runStreamTest(t, upstreamSSE, "glm-5.3-flash", true)

	if strings.Contains(out, `"id":"proxy-finish"`) {
		t.Errorf("上游已给出 finish_reason 时不应补帧:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"length"`) {
		t.Errorf("应保留上游 finish_reason:\n%s", out)
	}
}

// TestStreamAgentImmediateDone 验证 agent 流即时转发 [DONE] 且不注入补帧。
func TestStreamAgentImmediateDone(t *testing.T) {
	upstreamSSE := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"

	out := runStreamTest(t, upstreamSSE, "openpangu-2.0-pro", false)

	if strings.Contains(out, `"id":"proxy-finish"`) {
		t.Errorf("agent 通道不应补帧:\n%s", out)
	}
	if strings.Count(out, "data: [DONE]") != 1 {
		t.Errorf("[DONE] 应恰好出现一次:\n%s", out)
	}
}

// TestStreamAgentNormalizesOtherFinishReason 验证 agent 流的 "other" 归一化。
func TestStreamAgentNormalizesOtherFinishReason(t *testing.T) {
	upstreamSSE := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"other\"}]}\n\n" +
		"data: [DONE]\n\n"

	out := runStreamTest(t, upstreamSSE, "openpangu-2.0-pro", false)
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("other 应归一化为 stop:\n%s", out)
	}
}

// TestStreamAgentAppendsDoneWhenUpstreamTruncated 验证上游断流时补 [DONE]。
func TestStreamAgentAppendsDoneWhenUpstreamTruncated(t *testing.T) {
	upstreamSSE := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"

	out := runStreamTest(t, upstreamSSE, "openpangu-2.0-pro", false)
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("上游未发 [DONE] 时应补齐:\n%s", out)
	}
}

// runStreamTest 用假上游跑一次流式请求，返回代理输出的 SSE 文本。
func runStreamTest(t *testing.T, upstreamSSE, model string, benefit bool) string {
	t.Helper()

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer fake.Close()

	channel := catalog.ChannelAgent
	if benefit {
		channel = catalog.ChannelBenefit
	}
	srv := newTestServerFor(t, model, channel)
	// 直接把上游响应交给流式处理函数，绕开真实网络。
	resp, err := http.Get(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	srv.streamResponse(rec, resp, model, benefit)
	return rec.Body.String()
}

// TestChatNonStreamAggregatesSSE 验证 stream=false 时上游返回 SSE 也能聚合成 JSON。
func TestChatNonStreamAggregatesSSE(t *testing.T) {
	out := runChatTest(t, "openpangu-2.0-pro", false,
		"data: {\"id\":\"c1\",\"model\":\"openpangu-2.0-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"成\"}}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"功\"},\"finish_reason\":\"other\"}]}\n\n"+
			"data: [DONE]\n\n", "text/event-stream")

	if !strings.Contains(out, `"content":"成功"`) {
		t.Errorf("SSE 应被聚合为单个 JSON: %s", out)
	}
	if strings.Contains(out, "data:") {
		t.Errorf("非流式响应不应包含 SSE 帧: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("finish_reason 应归一化: %s", out)
	}
}

// TestChatNonStreamPassesThroughJSON 验证上游返回普通 JSON 时原样输出。
func TestChatNonStreamPassesThroughJSON(t *testing.T) {
	out := runChatTest(t, "openpangu-2.0-pro", false,
		`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		"application/json")

	if !strings.Contains(out, `"content":"ok"`) {
		t.Errorf("JSON 应原样透传: %s", out)
	}
}

// TestChatBenefitAddsSignedHeaders 验证 benefit 通道会带上三个必需头。
func TestChatBenefitAddsSignedHeaders(t *testing.T) {
	var seen http.Header
	out := runChatTestWithCapture(t, "glm-5.3-flash", false,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		"application/json", &seen)

	if seen.Get("maas_type") != "benefit" {
		t.Errorf("maas_type = %q, want benefit", seen.Get("maas_type"))
	}
	if seen.Get("model-id") != "glm-5.3-flash" {
		t.Errorf("model-id = %q", seen.Get("model-id"))
	}
	if seen.Get("model-name") != "glm-5.3-flash" {
		t.Errorf("model-name = %q", seen.Get("model-name"))
	}
	if !strings.Contains(seen.Get("Authorization"), "maas_type") {
		t.Errorf("三个头必须参与签名: %s", seen.Get("Authorization"))
	}
	if out == "" {
		t.Error("响应不应为空")
	}
}

// TestChatAgentOmitsBenefitHeaders 验证 agent 通道不带 benefit 头。
func TestChatAgentOmitsBenefitHeaders(t *testing.T) {
	var seen http.Header
	runChatTestWithCapture(t, "openpangu-2.0-pro", false,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		"application/json", &seen)

	if seen.Get("maas_type") != "" {
		t.Errorf("agent 通道不应带 maas_type: %q", seen.Get("maas_type"))
	}
	if strings.Contains(seen.Get("Authorization"), "maas_type") {
		t.Errorf("agent 通道签名不应包含 maas_type: %s", seen.Get("Authorization"))
	}
}

// TestChatUpstreamErrorReturns502 验证上游非 200 时返回 502 并透传细节。
func TestChatUpstreamErrorReturns502(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error_msg":"model is not registered"}`)
	}))
	defer fake.Close()

	srv := newTestServer(t, []catalog.Model{agentModel("openpangu-2.0-pro")})
	srv.chatURL = fake.URL

	body := `{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model is not registered") {
		t.Errorf("应透传上游错误细节: %s", rec.Body.String())
	}
}

// TestChatDefaultsModelWhenMissing 验证未指定 model 时使用首个可用模型。
func TestChatDefaultsModelWhenMissing(t *testing.T) {
	var seen http.Header
	out := runChatTestWithCapture(t, "openpangu-2.0-pro", false,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		"application/json", &seen)
	if out == "" {
		t.Fatal("响应为空")
	}
}

// TestChatToolsReachUpstreamAsStream 验证带工具的请求被整形为上游认的形态，
// 非流式客户端拿到的仍是聚合后的单个 JSON。
func TestChatToolsReachUpstreamAsStream(t *testing.T) {
	var seenBody map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seenBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			"data: {\"id\":\"c1\",\"model\":\"openpangu-2.0-pro\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null,"+
				"\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}]}}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer fake.Close()

	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.chatURL = fake.URL

	body := `{"model":"openpangu-2.0-pro","stream":false,"messages":[{"role":"user","content":"看下目录"}],` +
		`"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}}],` +
		`"tool_choice":{"type":"function","function":{"name":"bash"}}}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if seenBody["stream"] != true {
		t.Errorf("带工具时上游必须走流式: %v", seenBody["stream"])
	}
	if seenBody["tool_stream"] != true {
		t.Errorf("上游缺少 tool_stream=true: %v", seenBody["tool_stream"])
	}
	if seenBody["tool_choice"] != "required" {
		t.Errorf("对象形态 tool_choice 应被改为字符串 required, 得到 %v", seenBody["tool_choice"])
	}

	out := rec.Body.String()
	if strings.Contains(out, "data:") {
		t.Errorf("非流式客户端不应收到 SSE 帧: %s", out)
	}
	if !strings.Contains(out, `"tool_calls"`) {
		t.Errorf("聚合结果应保留 tool_calls: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason 应为 tool_calls: %s", out)
	}
}

// runChatTest 用假上游跑一次聊天请求，返回代理响应体。
func runChatTest(t *testing.T, model string, stream bool, upstreamBody, contentType string) string {
	t.Helper()
	return runChatTestWithCapture(t, model, stream, upstreamBody, contentType, nil)
}

func runChatTestWithCapture(t *testing.T, model string, stream bool, upstreamBody, contentType string, capture *http.Header) string {
	t.Helper()

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture = r.Header.Clone()
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer fake.Close()

	// 依据模型名推断通道：glm-/deepseek- 视为 benefit，其余视为 agent。
	channel := catalog.ChannelAgent
	if strings.HasPrefix(model, "glm-") || strings.HasPrefix(model, "deepseek-") {
		channel = catalog.ChannelBenefit
	}
	srv := newTestServerFor(t, model, channel)
	srv.chatURL = fake.URL

	payload := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if stream {
		payload["stream"] = true
	}
	encoded, _ := json.Marshal(payload)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(encoded)))
	return rec.Body.String()
}
