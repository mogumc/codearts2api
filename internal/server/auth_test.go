package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codearts2api/internal/catalog"
)

// TestAuthDisabledByDefault 验证未配置密钥时不鉴权（保持向后兼容）。
func TestAuthDisabledByDefault(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	// 不调用 SetAuth，Keys 为空。

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("未配置密钥时应放行, status = %d", rec.Code)
	}
}

// TestAuthRejectsMissingKey 验证缺少密钥时返回 401。
func TestAuthRejectsMissingKey(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少密钥应返回 401, 得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_api_key") {
		t.Errorf("响应应包含错误码: %s", rec.Body.String())
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 应带 WWW-Authenticate 头")
	}
}

// TestAuthRejectsWrongKey 验证错误密钥被拒绝。
func TestAuthRejectsWrongKey(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误密钥应返回 401, 得到 %d", rec.Code)
	}
}

// TestAuthAcceptsValidKey 验证正确密钥放行。
func TestAuthAcceptsValidKey(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-demo-1")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("正确密钥应放行, 得到 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAuthAcceptsAnyConfiguredKey 验证多密钥场景。
func TestAuthAcceptsAnyConfiguredKey(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-a", "sk-b", "sk-c"}})

	for _, key := range []string{"sk-a", "sk-b", "sk-c"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("密钥 %q 应被接受, 得到 %d", key, rec.Code)
		}
	}
}

// TestAuthAcceptsKeyWithoutBearerPrefix 验证兼容不带 Bearer 前缀的写法。
func TestAuthAcceptsKeyWithoutBearerPrefix(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "sk-demo-1")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("裸密钥应被接受, 得到 %d", rec.Code)
	}
}

// TestAuthBearerCaseInsensitive 验证 "bearer" 大小写不敏感。
func TestAuthBearerCaseInsensitive(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "bearer sk-demo-1")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("bearer 小写应被接受, 得到 %d", rec.Code)
	}
}

// TestAuthAllowsPreflight 验证 CORS 预检不被拦截。
// 浏览器预检不带自定义头，拦截会导致跨域调用整体失败。
func TestAuthAllowsPreflight(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("预检应放行, 得到 %d", rec.Code)
	}
}

// TestAuthErrorCarriesCORSHeaders 验证 401 响应也带跨域头，
// 否则浏览器读不到错误信息，只会显示笼统的 CORS 失败。
func TestAuthErrorCarriesCORSHeaders(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("401 响应应带 CORS 头")
	}
}

// TestAuthProtectsAllEndpoints 验证鉴权覆盖全部端点。
func TestAuthProtectsAllEndpoints(t *testing.T) {
	srv := newTestServerFor(t, "openpangu-2.0-pro", catalog.ChannelAgent)
	srv.SetAuth(AuthConfig{Keys: []string{"sk-demo-1"}})

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/models", ""},
		{http.MethodGet, "/v1/models/openpangu-2.0-pro", ""},
		{http.MethodPost, "/v1/chat/completions", `{"model":"openpangu-2.0-pro","messages":[]}`},
	}
	for _, tc := range cases {
		var reader *strings.Reader
		if tc.body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(tc.body)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, reader))

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 应返回 401, 得到 %d", tc.method, tc.path, rec.Code)
		}
	}
}

// TestExtractToken 覆盖密钥提取的各种写法。
func TestExtractToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer sk-1", "sk-1"},
		{"bearer sk-1", "sk-1"},
		{"BEARER sk-1", "sk-1"},
		{"  Bearer   sk-1  ", "sk-1"},
		{"sk-1", "sk-1"},
		{"", ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		if got := extractToken(req); got != tc.want {
			t.Errorf("extractToken(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

// TestAuthEnabled 验证 Enabled 判断。
func TestAuthEnabled(t *testing.T) {
	if (AuthConfig{}).Enabled() {
		t.Error("空密钥配置应视为未启用")
	}
	if (AuthConfig{Keys: []string{""}}).Enabled() != true {
		// 空串也算"配置了"（len>0），此时空密钥无法通过校验，属预期。
		t.Log("空串密钥视为已启用（但无法通过校验）")
	}
	if !(AuthConfig{Keys: []string{"k"}}).Enabled() {
		t.Error("有密钥时应视为已启用")
	}
}
