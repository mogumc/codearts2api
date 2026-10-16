package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"codearts2api/internal/signer"
)

// TestDoUsesSignedHostResolver 验证签名 host 解析器被真正应用。
//
// 这是中转场景的核心：连接地址与签名 host 必须解耦。
func TestDoUsesSignedHostResolver(t *testing.T) {
	var gotHost, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	const upstreamHost = "snap-access.cn-north-4.myhuaweicloud.com"
	client := New(signer.New("demo-ak", "demo-sk")).
		WithSignedHostResolver(func(string) string { return upstreamHost })

	resp, err := client.Do(context.Background(), http.MethodPost, server.URL+"/api/v2/chat/completions",
		[]byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 实际发出的 Host 必须是签名时用的 host（"签什么发什么"）。
	if gotHost != upstreamHost {
		t.Errorf("实际发出的 Host = %q, want %q", gotHost, upstreamHost)
	}
	if gotAuth == "" {
		t.Error("缺少 Authorization")
	}
}

// TestDoWithoutResolverUsesURLHost 验证未注入解析器时保持默认行为。
func TestDoWithoutResolverUsesURLHost(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New(signer.New("demo-ak", "demo-sk"))
	resp, err := client.Do(context.Background(), http.MethodGet, server.URL+"/v1/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 默认应使用 URL 自带的 host（httptest 是 127.0.0.1:port）。
	if gotHost == "" {
		t.Fatal("缺少 Host")
	}
	if gotHost == "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("未注入解析器时不应改写 Host, 得到 %q", gotHost)
	}
}

// TestDoResolverReturningEmptyKeepsDefault 验证解析器返回空串时回退默认行为。
func TestDoResolverReturningEmptyKeepsDefault(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New(signer.New("demo-ak", "demo-sk")).
		WithSignedHostResolver(func(string) string { return "" })

	resp, err := client.Do(context.Background(), http.MethodGet, server.URL+"/v1/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotHost != server.Listener.Addr().String() {
		t.Errorf("Host = %q, want %q", gotHost, server.Listener.Addr().String())
	}
}

// TestDoSetsUserAgent 验证 User-Agent 被设置（便于上游排查）。
func TestDoSetsUserAgent(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New(signer.New("demo-ak", "demo-sk"))
	resp, err := client.Do(context.Background(), http.MethodGet, server.URL+"/v1/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotUA == "" {
		t.Error("缺少 User-Agent")
	}
}
