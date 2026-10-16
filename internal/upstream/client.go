// Package upstream 封装带华为云签名的上游 HTTP 请求。
package upstream

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"codearts2api/internal/signer"
	"codearts2api/internal/version"
)

// responseHeaderTimeout 是等待上游响应头的超时时间。
//
// 这里刻意不使用 http.Client.Timeout：它是「整个请求」的总超时，会把长时间
// 流式输出误判为超时。改用 Transport 级的响应头超时，既能快速发现上游无响应，
// 又不会切断正常的长连接流。请求体的读写空闲由调用方的 context 控制。
const responseHeaderTimeout = 60 * time.Second

// Client 是带签名能力的 HTTP 客户端。
type Client struct {
	creds *signer.Credentials
	http  *http.Client

	// signedHostFor 按请求 URL 返回参与签名的 host；返回空串表示取 URL 自带 host。
	//
	// 通过反向代理/中转访问上游时需要它：上游网关按它认定的域名验签，
	// 而中转域名与上游真实域名不同，会导致验签失败（401 / APIG.0301）。
	// 做成回调是为了让 upstream 包不依赖 config 包。
	signedHostFor func(rawURL string) string
}

// New 构造上游客户端。
func New(creds *signer.Credentials) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
	return &Client{
		creds: creds,
		http:  &http.Client{Transport: transport},
	}
}

// WithSignedHostResolver 注入签名 host 解析函数（链式调用）。
//
// resolver 为 nil 时恢复默认行为（用请求 URL 的 host 签名）。
func (c *Client) WithSignedHostResolver(resolver func(rawURL string) string) *Client {
	c.signedHostFor = resolver
	return c
}

// Credentials 暴露底层签名器。
func (c *Client) Credentials() *signer.Credentials { return c.creds }

// signedHost 返回本次请求应使用的签名 host（空串表示用 URL 自带 host）。
func (c *Client) signedHost(rawURL string) string {
	if c.signedHostFor == nil {
		return ""
	}
	return strings.TrimSpace(c.signedHostFor(rawURL))
}

// Do 发送带 SDK-HMAC-SHA256 签名的请求，返回原始响应（响应体由调用方关闭）。
func (c *Client) Do(ctx context.Context, method, rawURL string, body []byte, extraHeaders map[string]string) (*http.Response, error) {
	headers := c.creds.SignForHost(method, rawURL, c.signedHost(rawURL), body, extraHeaders)

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		if name == "Authorization" {
			req.Header.Set("Authorization", value)
			continue
		}
		if name == "host" {
			// Host 是 Go 的特殊字段：通过 Header.Set 设置会被传输层忽略，
			// 必须写入 req.Host。显式赋值可保证实际发出的 Host 与签名中
			// 参与计算的 host 完全一致，避免二者不一致导致验签失败。
			req.Host = value
			continue
		}
		req.Header.Set(name, value)
	}
	// Content-Length 需显式给出，否则部分网关按 chunked 处理会导致签名不一致。
	if len(body) > 0 {
		req.ContentLength = int64(len(body))
	}
	// User-Agent 刻意不参与签名（签名头集合已在 Sign 内固定），
	// 只用于上游日志排查。
	req.Header.Set("User-Agent", version.UserAgent())
	return c.http.Do(req)
}
