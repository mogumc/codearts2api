// Package upstream 封装带华为云签名的上游 HTTP 请求。
package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"codearts2api/internal/signer"
	"codearts2api/internal/version"
)

// 上游身份头的两种形态，取值对齐官方客户端。
const (
	ClientModeCLI = "cli"
	ClientModeIDE = "ide"
)

// 官方客户端版本号。CLI 取码道 CLI 当前发行版，IDE 取 CodeArts Agent 插件版本。
const (
	cliVersion = "26.11.1"
	ideVersion = "26.9.902"
)

// ChatHeaders 返回聊天补全的固定请求头。
//
// 与官方 CodeArts Agent（AgentKernel）保持一致：除签名本身外，还要带上
// app-id / is_confidential / X-Language / Accept。这些头让上游把请求识别为
// IDE 会话流量，而不是一次匿名的通用调用——后者更容易被场景风控拦下
// （表现为 403 + InferHub.ModelArts.81011）。
//
// traceID 为空时按纳秒时间戳生成；它只是链路追踪标识，不参与业务判断。
func ChatHeaders(traceID string) map[string]string {
	if strings.TrimSpace(traceID) == "" {
		traceID = fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return map[string]string{
		"Content-Type":    "application/json",
		"Accept":          "text/event-stream",
		"X-Language":      "zh-cn",
		"app-id":          "CodeAgent3.0",
		"is_confidential": "false",
		"x-snap-traceid":  traceID,
	}
}

// IdentityHeaders 返回让上游把调用方识别成官方客户端的身份头。
//
// 服务端用量页面的「客户端」列读的就是 x-ot-client-type，缺省即显示 unknown；
// plugin-* / ide-* / x-ot-function 用于埋点与路由。取值来自码道 CLI 26.11.1
// 与 CodeArts Agent 26.9.902 的实际请求头：
//
//	CLI: client-type=CLI, client-version=26.11.1,function=agent-tui,plugin-name=CLI
//	IDE: client-type=IDE, client-version=26.9.902,function=agent-general,
//	     plugin-name=codemate_vscode, ide-name=VSCode
//
// mode 为空或无法识别时按 CLI 处理（码道 CLI 是当前主推的入口）。
// 这些头都参与签名，与官方客户端一致。
func IdentityHeaders(mode string) map[string]string {
	if strings.EqualFold(strings.TrimSpace(mode), ClientModeIDE) {
		return map[string]string{
			"plugin-name":         "codemate_vscode",
			"plugin-version":      ideVersion,
			"ide-name":            "VSCode",
			"ide-version":         ideVersion,
			"x-ot-client-type":    "IDE",
			"x-ot-client-version": ideVersion,
			"x-ot-function":       "agent-general",
		}
	}
	return map[string]string{
		"plugin-name":         "CLI",
		"plugin-version":      cliVersion,
		"x-ot-client-type":    "CLI",
		"x-ot-client-version": cliVersion,
		"x-ot-function":       "agent-tui",
	}
}

// IdentityUserAgent 返回与身份头匹配的 User-Agent。
//
// 上游不校验 UA（也不参与签名），但把它对齐能避免日志里出现自造标识。
func IdentityUserAgent(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), ClientModeIDE) {
		return "CodeArts-Agent/" + ideVersion
	}
	return "codearts/" + cliVersion
}

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

	// signedHost 是参与签名的 host，构造时固定。
	//
	// 通过反向代理访问上游时填上游真实域名：网关按它收到的 Host 验签，
	// 而代理域名与上游域名不同，会导致验签失败（401 / APIG.0301）。
	// 直接替换 BaseUrl 时留空，签名 host 自动取请求 URL 的 host。
	signedHost string

	// userAgent 为空时用 version.UserAgent()。只需在启动阶段设置一次。
	userAgent string
}

// New 构造上游客户端。signedHost 为空表示用请求 URL 自身的 host 签名。
func New(creds *signer.Credentials, signedHost string) *Client {
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
		creds:      creds,
		http:       &http.Client{Transport: transport},
		signedHost: strings.TrimSpace(signedHost),
	}
}

// Do 发送带 SDK-HMAC-SHA256 签名的请求，返回原始响应（响应体由调用方关闭）。
func (c *Client) Do(ctx context.Context, method, rawURL string, body []byte, extraHeaders map[string]string) (*http.Response, error) {
	return c.DoWithUnsigned(ctx, method, rawURL, body, extraHeaders, nil)
}

// SetUserAgent 覆盖请求的 User-Agent（空值忽略，回到默认）。
//
// 只在启动阶段调用，之后不再修改，因此不需要额外加锁。
func (c *Client) SetUserAgent(ua string) {
	if strings.TrimSpace(ua) != "" {
		c.userAgent = strings.TrimSpace(ua)
	}
}

// DoWithUnsigned 与 Do 相同，但在签名完成后再追加 unsignedHeaders。
//
// 上游只校验 Authorization 里的 SignedHeaders，未参与签名的头可以照常发送。
// Chat-Id / Session-Id 属于这一类：它们随会话变化，写进签名没有收益，
// 反而让签名头集合频繁变动。
func (c *Client) DoWithUnsigned(ctx context.Context, method, rawURL string, body []byte, signedHeaders, unsignedHeaders map[string]string) (*http.Response, error) {
	headers := c.creds.SignForHost(method, rawURL, c.signedHost, body, signedHeaders)

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
	for name, value := range unsignedHeaders {
		req.Header.Set(name, value)
	}
	// Content-Length 需显式给出，否则部分网关按 chunked 处理会导致签名不一致。
	if len(body) > 0 {
		req.ContentLength = int64(len(body))
	}
	// User-Agent 刻意不参与签名（签名头集合已在 Sign 内固定），
	// 只用于上游日志排查。
	ua := c.userAgent
	if ua == "" {
		ua = version.UserAgent()
	}
	req.Header.Set("User-Agent", ua)
	return c.http.Do(req)
}
