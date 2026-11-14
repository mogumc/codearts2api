// Package server 提供 OpenAI 兼容的 HTTP 接口。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codearts2api/internal/catalog"
	"codearts2api/internal/config"
	"codearts2api/internal/openai"
	"codearts2api/internal/upstream"
)

// nonStreamTimeout 是非流式请求的超时时间（与参考实现一致，放宽到 10 分钟）。
// 流式请求不设整体超时，仅靠客户端断开与响应头超时保护。
const nonStreamTimeout = 600 * time.Second

// upstreamErrorLimit 限制读取上游错误响应的字节数（只为拿到错误文案）。
const upstreamErrorLimit = 8 << 10

// Server 承载代理的全部路由与依赖。
type Server struct {
	cfg     *config.Config
	catalog *catalog.Catalog
	client  *upstream.Client

	// chatURL 是聊天补全的上游地址，取自 cfg.Endpoints，测试时可替换为假上游。
	chatURL string

	// auth 是下游鉴权配置；Keys 为空时不鉴权。
	auth AuthConfig

	// trimRetry 是内容审核命中后的降级重试条数（0 表示不重试）。
	//
	// 命中 81011 时丢掉早期历史、只发开头 system + 尾部 N 条再试一次。
	// 默认关闭：这会牺牲上下文完整性，属于「总比整轮失败好」的兜底。
	trimRetry int

	// autoClaim 在每天首个请求时顺带签到一次；为 nil 时不做任何事。
	// 调用点只做内存判断，真正的签到在后台 goroutine 执行。
	autoClaim func()
}

// New 构造代理服务。
//
// client 需已绑定 CodeArts 通道的签名 host（见 config.Endpoints.CodeArtsHost）。
func New(cfg *config.Config, cat *catalog.Catalog, client *upstream.Client) *Server {
	return &Server{
		cfg:       cfg,
		catalog:   cat,
		client:    client,
		chatURL:   cfg.Endpoints.Chat(),
		auth:      AuthConfig{Keys: cfg.ProxyKeys},
		trimRetry: cfg.TrimRetry,
	}
}

// SetAuth 替换下游鉴权配置（测试用）。
func (s *Server) SetAuth(cfg AuthConfig) {
	s.auth = cfg
}

// SetAutoClaim 注入自动签到钩子。传 nil 表示关闭。
func (s *Server) SetAutoClaim(hook func()) {
	s.autoClaim = hook
}

// triggerAutoClaim 在请求路径上触发自动签到判断。
//
// 这里只调用一个做内存判断的函数（未开启或已签到时立即返回），
// 真正的网络请求由钩子内部异步完成，因此不会拖慢用户请求。
func (s *Server) triggerAutoClaim() {
	if s.autoClaim != nil {
		s.autoClaim()
	}
}

// Handler 返回装配好路由与中间件的 http.Handler。
//
// 中间件顺序（由外到内）：CORS → 鉴权 → 路由。
// CORS 放在最外层，保证 401 响应也带跨域头，浏览器能读到错误信息。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModelDetail)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	return withCORS(withAuth(s.auth, mux))
}

// withCORS 为所有响应补充跨域头，并直接放行 OPTIONS 预检。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "", "")
		return
	}
	// 每天首个请求顺带签到一次（内存判断，异步执行）。
	s.triggerAutoClaim()
	s.catalog.RefreshIfNeeded(r.Context())

	models := s.catalog.Models()
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, modelPayload(m))
	}
	writeRawJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (s *Server) handleModelDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "", "")
		return
	}
	// 每天首个请求顺带签到一次（内存判断，异步执行）。
	s.triggerAutoClaim()
	s.catalog.RefreshIfNeeded(r.Context())

	modelID := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	model, ok := s.catalog.Lookup(modelID)
	if !ok {
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("model '%s' not found", modelID),
			"invalid_request_error", "model_not_found")
		return
	}
	writeRawJSON(w, http.StatusOK, modelPayload(model))
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "", "")
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "empty body", "", "")
		return
	}

	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", "", "")
		return
	}

	// 请求聊天时也按短缓存周期同步一次模型列表。
	s.catalog.RefreshIfNeeded(r.Context())

	model, _ := req["model"].(string)
	if model == "" {
		model = s.catalog.FirstID()
	}
	if !s.catalog.Contains(model) {
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("model '%s' 不在当前账号可用模型列表中", model),
			"invalid_request_error", "model_not_found")
		return
	}
	req["model"] = model

	// 到这里请求已确认合法，顺带做一次签到判断。
	// 放在校验之后，避免畸形请求或错误的模型名触发签到。
	s.triggerAutoClaim()

	// 客户端是否要流式，在整形之前取——整形对带工具的请求会强制上游走流式，
	// 不能让它改变对客户端的响应方式。
	wantStream, _ := req["stream"].(bool)

	// 会话标识：调用方显式给出的优先，否则按首条 user 消息派生。
	// conversation_id 不是 OpenAI 字段，读走后必须删掉，否则会被带给上游。
	chatID := openai.ChatID(explicitChatID(r, req), asSlice(req["messages"]))
	delete(req, "conversation_id")

	// 整形：上游的工具调用只在「流式 + tool_stream」下产出结构化 tool_calls，
	// 且 tool_choice 只接受字符串形态。非流式客户端由下面的聚合路径兜住。
	if err := openai.ShapeRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}

	// chat_id / prompt_cache_key 让上游把同一会话的请求串起来：复用 prompt
	// cache，并当作 IDE 会话续接，而不是每次都是全新的匿名调用。
	req["chat_id"] = chatID
	req["prompt_cache_key"] = chatID

	// 重新序列化 body，确保与签名时使用的字节完全一致。
	body, err := marshalCompact(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", "", "")
		return
	}

	// 固定头（app-id / X-Language / Accept 等）与身份头都必须参与签名，
	// 因此统一交给上游客户端处理。
	headers := upstream.ChatHeaders("")
	for name, value := range upstream.IdentityHeaders(s.cfg.ClientMode) {
		headers[name] = value
	}
	// Chat-Id / Session-Id 在签名之后追加，不进入 SignedHeaders。
	unsigned := map[string]string{
		"Chat-Id":    chatID,
		"Session-Id": openai.SessionID(chatID),
	}

	ctx := r.Context()
	if !wantStream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, nonStreamTimeout)
		defer cancel()
	}

	resp, fail := s.sendChat(ctx, body, headers, unsigned)
	// 内容审核命中时按配置做一次降级重试：丢掉早期历史再试。
	if fail != nil && fail.Code == openai.ContentFilter && s.trimRetry > 0 {
		if trimMessages(req, s.trimRetry) {
			if retryBody, encErr := marshalCompact(req); encErr == nil {
				if retryResp, retryFail := s.sendChat(ctx, retryBody, headers, unsigned); retryFail == nil {
					resp, fail = retryResp, nil
				}
			}
		}
	}
	if fail != nil {
		writeFailure(w, fail)
		return
	}
	defer resp.Body.Close()

	sse := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")

	// 上游偶尔在请求流式时返回普通 JSON（排队、重试或特定模型场景），
	// 也可能在非流式请求上返回 SSE。两种形态都在这里统一处理，保证错误
	// 能在写出 200 之前被识别出来。
	if !sse {
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream read error: "+err.Error(), "", "")
			return
		}
		result := openai.ParseUpstream(payload)
		if failure := embeddedFailure(result); failure != nil {
			writeFailure(w, failure)
			return
		}
		if wantStream {
			writeSingleSSEFrame(w, result)
			return
		}
		writeRawJSON(w, http.StatusOK, result)
		return
	}

	if wantStream {
		s.streamResponse(w, resp)
		return
	}

	// 非流式：上游返回 SSE，统一聚合成单个 JSON。
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream read error: "+err.Error(), "", "")
		return
	}
	result := openai.ParseUpstream(payload)
	if failure := embeddedFailure(result); failure != nil {
		writeFailure(w, failure)
		return
	}
	writeRawJSON(w, http.StatusOK, result)
}

// sendChat 向上游发起一次聊天请求，并对流式响应做首帧探测。
//
// 返回的 fail 非 nil 时响应体已关闭，调用方只需把错误写给下游。
func (s *Server) sendChat(ctx context.Context, body []byte, headers, unsigned map[string]string) (*http.Response, *upstreamFailure) {
	resp, err := s.client.DoWithUnsigned(ctx, http.MethodPost, s.chatURL, body, headers, unsigned)
	if err != nil {
		return nil, &upstreamFailure{
			Status:  http.StatusBadGateway,
			Type:    "upstream_error",
			Message: "upstream error: " + err.Error(),
		}
	}

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamErrorLimit))
		_ = resp.Body.Close()
		code, message := openai.ExtractUpstreamError(detail)
		return nil, classifyFailure(resp.StatusCode, code, message, string(detail))
	}

	// 内容审核等业务错误常以 HTTP 200 + 首帧内嵌 error_code 返回，
	// 必须在写出响应头之前识别，否则客户端只会看到一段空回复。
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		reader, fail := preflightStream(resp.Body)
		if fail != nil {
			return nil, fail
		}
		resp.Body = reader
	}
	return resp, nil
}

// embeddedFailure 把聚合结果里的 error 对象翻译成下游错误；没有错误时返回 nil。
func embeddedFailure(result any) *upstreamFailure {
	obj, ok := result.(map[string]any)
	if !ok {
		return nil
	}
	code, message, found := openai.ExtractEmbeddedError(obj)
	if !found {
		return nil
	}
	return classifyFailure(http.StatusOK, code, message, "")
}

// explicitChatID 读取调用方显式给出的会话标识。
func explicitChatID(r *http.Request, req map[string]any) string {
	if value := strings.TrimSpace(r.Header.Get("X-Codearts-Chat-Id")); value != "" {
		return value
	}
	value, _ := req["conversation_id"].(string)
	return value
}

// trimMessages 把消息裁成「开头的 system + 尾部 keep 条」，用于审核命中的降级重试。
//
// 命中的诱因常常埋在被反复重放的历史里：一条旧消息会被每一轮重新送审，于是
// 整个会话永久失败。丢掉早期历史再试一次有机会绕开，但模型也看不到完整上下文，
// 因此默认关闭（CODEARTS_TRIM_RETRY=N 才启用）。
func trimMessages(req map[string]any, keep int) bool {
	msgs, _ := req["messages"].([]any)
	if keep <= 0 || len(msgs) <= keep {
		return false
	}

	system := 0
	for system < len(msgs) {
		msg, _ := msgs[system].(map[string]any)
		if msg == nil {
			break
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			break
		}
		system++
	}
	if len(msgs)-keep <= system {
		// 裁完连一条可丢弃的历史都没有，重试没有意义。
		return false
	}

	trimmed := make([]any, 0, system+keep)
	trimmed = append(trimmed, msgs[:system]...)
	trimmed = append(trimmed, msgs[len(msgs)-keep:]...)
	req["messages"] = trimmed
	return openai.ShapeRequest(req) == nil
}

// streamResponse 严格按 OpenAI SSE 转发上游流（调用方已确认上游返回的是 SSE）。
//
// 上游异常断流、没有发送 [DONE] 时补一个，保证客户端状态机完整。
func (s *Server) streamResponse(w http.ResponseWriter, resp *http.Response) {
	flusher, _ := w.(http.Flusher)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sawDone := false

	reader := newSSEReader(resp.Body)
	for {
		line, err := reader.readLine()
		if line != nil {
			s.handleStreamLine(w, flusher, *line, &sawDone)
		}
		if err != nil {
			break
		}
	}

	if !sawDone {
		writeSSEDone(w)
		flush(flusher)
	}
}

// writeSingleSSEFrame 把单个聚合结果包装成合法的 SSE 流输出。
//
// 用于「客户端要流式、上游却回了普通 JSON」的场景；直接透传会让客户端把
// JSON 当成一帧 data 去解析，包装后至少状态机是完整的。
func writeSingleSSEFrame(w http.ResponseWriter, result any) {
	flusher, _ := w.(http.Flusher)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	encoded, err := marshalCompact(result)
	if err != nil {
		return
	}
	writeSSE(w, encoded)
	writeSSEDone(w)
	flush(flusher)
}

// handleStreamLine 处理上游单行 SSE，并按需转发。
//
// [DONE] 立即透传；上游重复发送时只转发一次。
func (s *Server) handleStreamLine(w io.Writer, flusher http.Flusher, line string, sawDone *bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if trimmed == "" {
		return
	}
	if !strings.HasPrefix(trimmed, "data:") {
		// 注释行（以 ":" 开头）按原样透传，保持与参考实现一致。
		if strings.HasPrefix(trimmed, ":") {
			fmt.Fprintf(w, "%s\n\n", trimmed)
			flush(flusher)
		}
		return
	}

	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "[DONE]" {
		if !*sawDone {
			writeSSEDone(w)
			flush(flusher)
			*sawDone = true
		}
		return
	}

	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return
	}
	for _, item := range asSlice(chunk["choices"]) {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if reason, exists := choice["finish_reason"]; exists && reason != nil && reason == "other" {
			choice["finish_reason"] = "stop"
		}
		// 部分模型会发送 content:null、reasoning_content 有值。Agent Runtime
		// 只接受字符串 content，null chunk 会被误判为无响应，这里统一规范化。
		if delta, ok := choice["delta"].(map[string]any); ok {
			if value, exists := delta["content"]; exists && value == nil {
				delta["content"] = ""
			}
		}
	}
	if encoded, err := marshalCompact(chunk); err == nil {
		writeSSE(w, encoded)
		flush(flusher)
	}
}

// modelPayload 生成 OpenAI 模型对象，并附带常见能力元数据。
func modelPayload(m catalog.Model) map[string]any {
	payload := map[string]any{
		"id":          m.ID,
		"object":      "model",
		"created":     0,
		"owned_by":    "codearts",
		"name":        firstNonEmpty(m.Name, m.ID),
		"description": m.Description,
		// 常见 OpenAI 兼容客户端会读取其中一部分；未知字段会被安全忽略。
		"supports_images":           m.SupportsImage,
		"vision":                    m.SupportsImage,
		"supports_vision":           m.SupportsImage,
		"supports_function_calling": true,
		"supports_tool_calling":     true,
		"supports_reasoning":        true,
		"input_modalities":          modalities(m.SupportsImage),
		"output_modalities":         []string{"text"},
		"supported_parameters": []string{
			"temperature", "top_p", "max_tokens", "stream",
			"tools", "tool_choice", "response_format",
		},
	}
	if m.ContextWindow != nil {
		payload["context_window"] = *m.ContextWindow
		payload["context_length"] = *m.ContextWindow
	}
	if m.MaxTokens != nil {
		payload["max_tokens"] = *m.MaxTokens
		payload["max_output_tokens"] = *m.MaxTokens
	}
	if m.EnableQueue {
		payload["enable_queue"] = true
	}
	return payload
}

func modalities(supportsImages bool) []string {
	if supportsImages {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}
