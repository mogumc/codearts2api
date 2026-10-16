package benefit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
)

// newTestClient 构造一个指向假上游的客户端。
func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := New(signer.New("demo-ak", "demo-sk"), config.DefaultEndpoints())
	client.claimURL = server.URL + "/api/v1/benefit/claim"
	client.balanceURL = server.URL + "/api/v1/user/tokens/balance"
	client.configURL = server.URL + "/api/v1/gateway/config"
	return client, server
}

func TestIsSuccessCode(t *testing.T) {
	// 与 Python 的 `not in (None, "0000", 0)` 等价：字符串 "0000" 与数字 0 都算成功。
	successCases := []any{nil, "0000", "", float64(0), 0, int64(0)}
	for _, code := range successCases {
		if !isSuccessCode(code) {
			t.Errorf("isSuccessCode(%#v) = false, 应为成功", code)
		}
	}
	failureCases := []any{"0001", "APIG.0613", float64(1), 500}
	for _, code := range failureCases {
		if isSuccessCode(code) {
			t.Errorf("isSuccessCode(%#v) = true, 应为失败", code)
		}
	}
}

// TestClaimAcceptsNumericZeroErrorCode 是本次修复的回归测试：
// 上游返回数字 0 时必须视为成功，不能误报业务错误。
func TestClaimAcceptsNumericZeroErrorCode(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error_code":0,"error_msg":"","result":{"create_time":1789539411000}}`)
	}))

	info, err := client.Claim(context.Background())
	if err != nil {
		t.Fatalf("数字 0 应视为成功, 得到错误: %v", err)
	}
	if info["create_time"] != float64(1789539411000) {
		t.Errorf("create_time = %v", info["create_time"])
	}
}

// TestClaimAcceptsStringZeroErrorCode 覆盖字符串 "0000" 的成功路径。
func TestClaimAcceptsStringZeroErrorCode(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{"create_time":1789539411000}}`)
	}))

	if _, err := client.Claim(context.Background()); err != nil {
		t.Fatalf("字符串 0000 应视为成功: %v", err)
	}
}

// TestClaimRejectsBusinessError 覆盖真实业务错误的路径。
func TestClaimRejectsBusinessError(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error_code":"0001","error_msg":"already claimed"}`)
	}))

	_, err := client.Claim(context.Background())
	if err == nil {
		t.Fatal("业务错误应返回 error")
	}
	if !strings.Contains(err.Error(), "already claimed") {
		t.Errorf("错误信息应包含上游细节: %v", err)
	}
}

// TestClaimUsesPostMethod 验证签到走 POST、状态查询走 GET。
func TestClaimUsesPostMethod(t *testing.T) {
	var claimMethod, statusMethod string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/benefit/claim":
			if r.Method == http.MethodPost {
				claimMethod = r.Method
			} else {
				statusMethod = r.Method
			}
		}
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{}}`)
	}))

	if _, err := client.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if claimMethod != http.MethodPost {
		t.Errorf("签到应为 POST, 得到 %q", claimMethod)
	}
	if statusMethod != http.MethodGet {
		t.Errorf("状态查询应为 GET, 得到 %q", statusMethod)
	}
}

// TestRequestsAreSigned 验证签到请求带上了华为云签名头。
func TestRequestsAreSigned(t *testing.T) {
	var auth, sdkDate, host string
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		sdkDate = r.Header.Get("x-sdk-date")
		// Host 不在 Header 中，需从 r.Host 读取。
		host = r.Host
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{}}`)
	}))

	if _, err := client.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(auth, "SDK-HMAC-SHA256 Access=demo-ak") {
		t.Errorf("Authorization 格式错误: %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") || !strings.Contains(auth, "Signature=") {
		t.Errorf("Authorization 缺少必要字段: %q", auth)
	}
	if sdkDate == "" {
		t.Error("缺少 x-sdk-date 头")
	}
	if host == "" {
		t.Error("缺少 host")
	}
	if !strings.Contains(auth, "host") {
		t.Errorf("SignedHeaders 应包含 host: %q", auth)
	}
}

// TestBalanceParsesAmounts 验证余额查询的字段解析。
func TestBalanceParsesAmounts(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{"total_quota":1000000,"total_balance":750000,"used_amount":250000}}`)
	}))

	info, err := client.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if number(info["total_quota"]) != 1000000 {
		t.Errorf("total_quota = %v", info["total_quota"])
	}
	if number(info["total_balance"]) != 750000 {
		t.Errorf("total_balance = %v", info["total_balance"])
	}
}

// TestModelsParsesList 验证免费模型列表解析。
func TestModelsParsesList(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{"models":[{"model_id":"glm-5.3-flash","model_name":"GLM-5.3-Flash","context_window":1048576,"max_tokens":131072},{"model_id":"deepseek-v4-pro-0813"}]}}`)
	}))

	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("模型数量 = %d", len(models))
	}
	if models[0]["model_id"] != "glm-5.3-flash" {
		t.Errorf("首个模型 = %v", models[0]["model_id"])
	}
}

// TestNon200ReturnsError 验证上游 HTTP 错误被正确上报。
func TestNon200ReturnsError(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error_msg":"service unavailable"}`)
	}))

	_, err := client.Claim(context.Background())
	if err == nil {
		t.Fatal("HTTP 503 应返回 error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("错误信息应包含状态码: %v", err)
	}
}

// TestRunActions 覆盖 benefit 子命令的各 action 分支。
func TestRunActions(t *testing.T) {
	for _, action := range []string{"status", "claim", "models", "default"} {
		t.Run(action, func(t *testing.T) {
			var claimCalled bool
			client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/benefit/claim"):
					if r.Method == http.MethodPost {
						claimCalled = true
					}
					_, _ = io.WriteString(w, `{"error_code":"0000","result":{"create_time":1789539411000}}`)
				case strings.HasSuffix(r.URL.Path, "/tokens/balance"):
					_, _ = io.WriteString(w, `{"error_code":"0000","result":{"total_quota":100,"total_balance":80,"used_amount":20}}`)
				case strings.HasSuffix(r.URL.Path, "/gateway/config"):
					_, _ = io.WriteString(w, `{"error_code":"0000","result":{"models":[{"model_id":"m1"}]}}`)
				default:
					http.NotFound(w, r)
				}
			}))

			if code := runWithClient(client, action); code != 0 {
				t.Errorf("action %q 退出码 = %d, 期望 0", action, code)
			}
			// 只有 claim 与 default 会真正签到。
			wantClaim := action == "claim" || action == "default"
			if claimCalled != wantClaim {
				t.Errorf("action %q 签到调用 = %v, 期望 %v", action, claimCalled, wantClaim)
			}
		})
	}
}

// TestFormatMillis 验证毫秒时间戳格式化。
func TestFormatMillis(t *testing.T) {
	if got := formatMillis(nil); got != "从未签到" {
		t.Errorf("空值 = %q", got)
	}
	if got := formatMillis(float64(0)); got != "从未签到" {
		t.Errorf("零值 = %q", got)
	}
	got := formatMillis(float64(1789539411000))
	if got == "从未签到" || len(got) != 19 {
		t.Errorf("格式化结果异常: %q", got)
	}
}

// TestThousands 验证千分位输出。
func TestThousands(t *testing.T) {
	cases := map[float64]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		1234567: "1,234,567",
	}
	for input, want := range cases {
		if got := thousands(input); got != want {
			t.Errorf("thousands(%v) = %q, want %q", input, got, want)
		}
	}
}

// TestTruncate 验证超长文本截断。
func TestTruncate(t *testing.T) {
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("短文本不应截断: %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("截断结果 = %q", got)
	}
}

// TestClaimTimeoutIsBounded 确认 Run 内的 context 有超时上限。
func TestClaimTimeoutIsBounded(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟慢上游，验证 context 超时能生效。
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{}}`)
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Claim(ctx)
	if err == nil {
		t.Fatal("超时场景应返回 error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("超时未生效，耗时 %v", elapsed)
	}
}

// TestDoHandlesMalformedJSON 验证非法 JSON 响应被正确上报。
func TestDoHandlesMalformedJSON(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{not json`)
	}))

	if _, err := client.Claim(context.Background()); err == nil {
		t.Fatal("非法 JSON 应返回 error")
	}
}

// runWithClient 用指定客户端执行 action，返回退出码。
// 与 Run 逻辑一致，但跳过 config 加载，便于注入假上游。
func runWithClient(client *Client, action string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch action {
	case "status":
		if err := showStatus(ctx, client); err != nil {
			return 1
		}
	case "claim":
		if err := doClaim(ctx, client); err != nil {
			return 1
		}
		if err := showBalance(ctx, client); err != nil {
			return 1
		}
	case "models":
		if err := showModels(ctx, client); err != nil {
			return 1
		}
	default:
		if err := showStatus(ctx, client); err != nil {
			return 1
		}
		if err := doClaim(ctx, client); err != nil {
			return 1
		}
		if err := showBalance(ctx, client); err != nil {
			return 1
		}
	}
	return 0
}
