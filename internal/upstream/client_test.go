package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"codearts2api/internal/signer"
)

// TestDoUsesConfiguredSignedHost 验证构造时指定的签名 host 被真正应用。
//
// 这是「代理 + host 覆盖」场景的核心：连接地址与签名 host 必须解耦。
func TestDoUsesConfiguredSignedHost(t *testing.T) {
	var gotHost, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	const upstreamHost = "snap-access.cn-north-4.myhuaweicloud.com"
	client := New(signer.New("demo-ak", "demo-sk"), upstreamHost)

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

// TestDoWithoutSignedHostUsesURLHost 验证未指定 host 时用请求 URL 的 host 签名。
//
// 这是「替换 BaseUrl」场景：签名 host 自动跟随新域名，无需额外配置。
func TestDoWithoutSignedHostUsesURLHost(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New(signer.New("demo-ak", "demo-sk"), "")
	resp, err := client.Do(context.Background(), http.MethodGet, server.URL+"/v1/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// httptest 的 host 是 127.0.0.1:port，应原样使用。
	if gotHost != server.Listener.Addr().String() {
		t.Errorf("Host = %q, want %q", gotHost, server.Listener.Addr().String())
	}
}

// TestDoTrimsSignedHostWhitespace 验证配置里多余的空白不会污染签名 host。
func TestDoTrimsSignedHostWhitespace(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New(signer.New("demo-ak", "demo-sk"), "  upstream.example.com  ")
	resp, err := client.Do(context.Background(), http.MethodGet, server.URL+"/v1/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotHost != "upstream.example.com" {
		t.Errorf("Host = %q, want upstream.example.com", gotHost)
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

	client := New(signer.New("demo-ak", "demo-sk"), "")
	resp, err := client.Do(context.Background(), http.MethodGet, server.URL+"/v1/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotUA == "" {
		t.Error("缺少 User-Agent")
	}
}
