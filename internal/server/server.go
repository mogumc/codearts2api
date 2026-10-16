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

// Server 承载代理的全部路由与依赖。
type Server struct {
	cfg     *config.Config
	catalog *catalog.Catalog
	client  *upstream.Client

	// chatURL 是聊天补全的上游地址，取自 cfg.Endpoints，测试时可替换为假上游。
	chatURL string

	// auth 是下游鉴权配置；Keys 为空时不鉴权。
	auth AuthConfig

	// autoClaim 在每天首个请求时顺带签到一次；为 nil 时不做任何事。
	// 调用点只做内存判断，真正的签到在后台 goroutine 执行。
	autoClaim func()
}

// New 构造代理服务。
//
// client 需已绑定 CodeArts 通道的签名 host（见 config.Endpoints.CodeArtsHost）。
func New(cfg *config.Config, cat *catalog.Catalog, client *upstream.Client) *Server {
	return &Server{
		cfg:     cfg,
		catalog: cat,
		client:  client,
		chatURL: cfg.Endpoints.Chat(),
		auth:    AuthConfig{Keys: cfg.ProxyKeys},
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

	// 通道判断：AgentCenter 老套餐模型用基础签名；其余（免费 benefit 模型）
	// 必须带 maas_type/model-id/model-name 三个签名头，否则上游报未注册。
	benefit := !s.catalog.IsAgentChannel(model)
	wantStream, _ := req["stream"].(bool)

	// 重新序列化 body，确保与签名时使用的字节完全一致。
	body, err := marshalCompact(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", "", "")
		return
	}

	// benefit 通道的三个头必须参与签名，因此交由上游客户端统一签名。
	extra := map[string]string{}
	if benefit && model != "" {
		extra["maas_type"] = "benefit"
		extra["model-id"] = model
		extra["model-name"] = model
	}

	ctx := r.Context()
	if !wantStream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, nonStreamTimeout)
		defer cancel()
	}

	resp, err := s.client.Do(ctx, http.MethodPost, s.chatURL, body, extra)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream error: "+err.Error(), "", "")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		writeError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream %d: %s", resp.StatusCode, string(detail)), "", "")
		return
	}

	if wantStream {
		s.streamResponse(w, resp, model, benefit)
		return
	}

	// 非流式：无论上游返回 JSON 还是 SSE，都统一聚合成单个 JSON。
	// CodeArts 在排队、重试或特定模型场景下可能即使请求 stream=false 仍返回
	// text/event-stream，直接转发会导致客户端把 data: 当作 JSON 解析。
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream read error: "+err.Error(), "", "")
		return
	}
	result := openai.ParseUpstream(payload)
	if obj, ok := result.(map[string]any); ok {
		if _, hasError := obj["error"]; hasError {
			writeRawJSON(w, http.StatusBadGateway, result)
			return
		}
	}
	writeRawJSON(w, http.StatusOK, result)
}

// streamResponse 严格按 OpenAI SSE 转发上游流。
//
// benefit 通道会补齐缺失的 finish_reason 与 [DONE]，并把 content:null 规范化为
// 空字符串（部分 Agent Runtime 只接受字符串 content，会把 null chunk 误判为无响应）；
// agent 通道保持即时转发行为，避免改变既有客户端状态机。
func (s *Server) streamResponse(w http.ResponseWriter, resp *http.Response, model string, benefit bool) {
	flusher, _ := w.(http.Flusher)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	// 上游偶尔会在请求 stream=true 时返回普通 JSON，包装成单个 SSE 帧。
	if !strings.Contains(contentType, "text/event-stream") {
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			return
		}
		result := openai.ParseUpstream(payload)
		if obj, ok := result.(map[string]any); ok {
			for _, item := range asSlice(obj["choices"]) {
				if choice, ok := item.(map[string]any); ok {
					choice["finish_reason"] = openai.NormalizeFinishReason(choice["finish_reason"])
				}
			}
		}
		if encoded, err := marshalCompact(result); err == nil {
			writeSSE(w, encoded)
			writeSSEDone(w)
			flush(flusher)
		}
		return
	}

	sawDone := false
	sawFinishReason := false

	reader := newSSEReader(resp.Body)
	for {
		line, err := reader.readLine()
		if line != nil {
			s.handleStreamLine(w, flusher, *line, benefit, &sawDone, &sawFinishReason)
		}
		if err != nil {
			// 上游异常断流时不中断响应，交给下面的补帧逻辑收尾。
			break
		}
	}

	if benefit {
		// 只对免费 benefit 模型补齐完全缺失的 finish_reason；不重发任何上游 chunk。
		if !sawFinishReason {
			writeSSE(w, finishFrame(model))
		}
		// 上游 [DONE] 被延迟到这里，确保补块在 [DONE] 之前。
		writeSSEDone(w)
		flush(flusher)
		return
	}
	// agent 分支保持原有行为：上游异常断流、没有发送 [DONE] 时补一个。
	if !sawDone {
		writeSSEDone(w)
		flush(flusher)
	}
}

// handleStreamLine 处理上游单行 SSE，并按需转发。
//
// benefit 通道延迟转发 [DONE]（等补帧完成后再发）；agent 通道收到 [DONE] 立即
// 转发，保持与参考实现一致的即时行为。
func (s *Server) handleStreamLine(w io.Writer, flusher http.Flusher, line string, benefit bool, sawDone, sawFinishReason *bool) {
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
		if !benefit {
			if !*sawDone {
				writeSSEDone(w)
				flush(flusher)
				*sawDone = true
			}
		} else {
			// 延迟到补帧之后再发。
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
		if reason, exists := choice["finish_reason"]; exists && reason != nil {
			*sawFinishReason = true
			if reason == "other" {
				choice["finish_reason"] = "stop"
			}
		}
		// DeepSeek benefit 流会发送 content:null、reasoning_content 有值。
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

// finishFrame 生成补齐用的结束帧。
func finishFrame(model string) []byte {
	frame := map[string]any{
		"id":      openai.NewChunkID(),
		"object":  "chat.completion.chunk",
		"created": openai.Now(),
		"model":   model,
		"choices": []any{
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
		},
	}
	encoded, _ := marshalCompact(frame)
	return encoded
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
