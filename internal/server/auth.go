package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// authHeaderPrefix 是客户端密钥的承载格式：Authorization: Bearer <key>。
const authHeaderPrefix = "Bearer "

// AuthConfig 描述下游鉴权配置。
type AuthConfig struct {
	// Keys 是允许的密钥集合。为空表示不鉴权（默认，保持向后兼容）。
	Keys []string
}

// Enabled 返回是否启用了鉴权。
func (a AuthConfig) Enabled() bool { return len(a.Keys) > 0 }

// withAuth 包装 handler，校验下游密钥。
//
// 未配置密钥时直接放行（默认行为，方便本机使用）。
// 配置了密钥后，所有 /v1/* 请求都必须携带正确的 Bearer 密钥。
//
// 不处理 OPTIONS 预检：外层 withCORS 已经短路了预检请求，进不到这里。
// 预检本身也不带自定义头，鉴权层即使收到也不该拦。
func withAuth(cfg AuthConfig, next http.Handler) http.Handler {
	if !cfg.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.authorized(r) {
			writeAuthError(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorized 校验请求携带的密钥。
func (a AuthConfig) authorized(r *http.Request) bool {
	token := extractToken(r)
	if token == "" {
		return false
	}
	// 逐个用常量时间比较，避免通过响应耗时逐字节猜测密钥。
	// 不提前 return，保证比较次数与密钥数量一致。
	matched := false
	for _, key := range a.Keys {
		if subtle.ConstantTimeCompare([]byte(token), []byte(key)) == 1 {
			matched = true
		}
	}
	return matched
}

// extractToken 从 Authorization 头提取密钥，兼容不带 "Bearer " 前缀的写法。
func extractToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	if len(header) > len(authHeaderPrefix) &&
		strings.EqualFold(header[:len(authHeaderPrefix)], authHeaderPrefix) {
		return strings.TrimSpace(header[len(authHeaderPrefix):])
	}
	return header
}

// writeAuthError 返回 OpenAI 风格的 401。
func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="codearts2api"`)
	body := map[string]any{
		"error": map[string]any{
			"message": "无效的 API Key：请在 Authorization 头携带 Bearer <key>",
			"type":    "invalid_request_error",
			"code":    "invalid_api_key",
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write(payload)
}
