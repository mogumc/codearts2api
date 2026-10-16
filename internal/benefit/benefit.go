// Package benefit 实现 CodeArts 免费额度签到与查询。
//
// 对应 IDE 里的免费模型福利机制（opengw.developer.huaweicloud.com）：
//
//	GET  /api/v1/benefit/claim        签到状态（返回上次签到时间）
//	POST /api/v1/benefit/claim        签到领取当日免费额度（空 body）
//	GET  /api/v1/user/tokens/balance  查询免费额度余额
//	GET  /api/v1/gateway/config       免费模型列表
//
// 配合 Windows 计划任务可实现每日自动签到，例如每天 09:05：
//
//	schtasks /create /tn CodeArtsBenefit /tr "codearts2api.exe benefit claim" /sc daily /st 09:05
package benefit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
	"codearts2api/internal/upstream"
)

// Client 是免费额度接口客户端。
type Client struct {
	client *upstream.Client

	// 端点可在测试中替换；默认取 config 中的常量。
	claimURL   string
	balanceURL string
	configURL  string
}

// New 构造免费额度客户端，端点取自 cfg。
//
// 签名 host 在构造时固定为 OpenGWHost：替换 BaseUrl 时自动跟随新域名，
// 用代理时由 CODEARTS_OPENGW_BASE_HOST 显式覆盖。
func New(creds *signer.Credentials, endpoints config.Endpoints) *Client {
	return &Client{
		client:     upstream.New(creds, endpoints.OpenGWHost()),
		claimURL:   endpoints.BenefitClaim(),
		balanceURL: endpoints.Balance(),
		configURL:  endpoints.GatewayConfig(),
	}
}

// Status 查询签到状态，create_time 即最近一次签到时间（毫秒时间戳）。
func (c *Client) Status(ctx context.Context) (map[string]any, error) {
	return c.get(ctx, c.claimURL)
}

// Claim 签到领取当日免费额度。
func (c *Client) Claim(ctx context.Context) (map[string]any, error) {
	return c.do(ctx, http.MethodPost, c.claimURL)
}

// Balance 查询免费额度余额。
func (c *Client) Balance(ctx context.Context) (map[string]any, error) {
	return c.get(ctx, c.balanceURL)
}

// Models 查询免费模型列表。
func (c *Client) Models(ctx context.Context) ([]map[string]any, error) {
	result, err := c.get(ctx, c.configURL)
	if err != nil {
		return nil, err
	}
	raw, _ := result["models"].([]any)
	models := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			models = append(models, m)
		}
	}
	return models, nil
}

func (c *Client) get(ctx context.Context, url string) (map[string]any, error) {
	return c.do(ctx, http.MethodGet, url)
}

func (c *Client) do(ctx context.Context, method, url string) (map[string]any, error) {
	resp, err := c.client.Do(ctx, method, url, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	if !isSuccessCode(payload["error_code"]) {
		return nil, fmt.Errorf("业务错误 %v: %v", payload["error_code"], payload["error_msg"])
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

// isSuccessCode 判断 opengw 的业务返回码是否表示成功。
//
// 与 Python 参考实现等价：data.get("error_code") not in (None, "0000", 0)。
// 上游既可能返回字符串 "0000"，也可能返回数字 0，两者都必须视为成功——
// 只判断字符串会把数字 0 误判成业务错误。
func isSuccessCode(code any) bool {
	switch v := code.(type) {
	case nil:
		return true
	case string:
		return v == "" || v == "0000"
	case float64:
		return v == 0
	case int:
		return v == 0
	case int64:
		return v == 0
	}
	return false
}

// Run 执行 benefit 子命令，返回进程退出码。
func Run(cfg *config.Config, creds *signer.Credentials, action string) int {
	client := New(creds, cfg.Endpoints)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch action {
	case "status":
		if err := showStatus(ctx, client); err != nil {
			return fail(err)
		}
	case "claim":
		if err := doClaim(ctx, client); err != nil {
			return fail(err)
		}
		if err := showBalance(ctx, client); err != nil {
			return fail(err)
		}
	case "models":
		fmt.Println("免费模型列表:")
		if err := showModels(ctx, client); err != nil {
			return fail(err)
		}
	default:
		// 默认：状态 + 签到 + 余额
		if err := showStatus(ctx, client); err != nil {
			return fail(err)
		}
		if err := doClaim(ctx, client); err != nil {
			return fail(err)
		}
		if err := showBalance(ctx, client); err != nil {
			return fail(err)
		}
	}
	return 0
}

func fail(err error) int {
	fmt.Println("失败:", err)
	return 1
}

func showStatus(ctx context.Context, client *Client) error {
	info, err := client.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("最近签到时间: %s\n", formatMillis(info["create_time"]))
	return nil
}

func doClaim(ctx context.Context, client *Client) error {
	info, err := client.Claim(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("签到成功，最近签到时间: %s\n", formatMillis(info["create_time"]))
	return nil
}

func showBalance(ctx context.Context, client *Client) error {
	info, err := client.Balance(ctx)
	if err != nil {
		return err
	}
	total := number(info["total_quota"])
	remain := number(info["total_balance"])
	used := number(info["used_amount"])
	percent := 0.0
	if total > 0 {
		percent = remain / total * 100
	}
	fmt.Printf("免费额度余额: %s / %s（已用 %s，剩余 %.1f%%）\n",
		thousands(remain), thousands(total), thousands(used), percent)
	return nil
}

func showModels(ctx context.Context, client *Client) error {
	models, err := client.Models(ctx)
	if err != nil {
		return err
	}
	for _, m := range models {
		id, _ := m["model_id"].(string)
		ctxWindow := number(m["context_window"])
		maxTokens := number(m["max_tokens"])
		if ctxWindow > 0 {
			fmt.Printf("  %s  (上下文 %s / 最大输出 %s)\n",
				id, thousands(ctxWindow), thousands(maxTokens))
			continue
		}
		fmt.Printf("  %s\n", id)
	}
	return nil
}

// formatMillis 把毫秒时间戳格式化为本地时间字符串。
func formatMillis(value any) string {
	ms := int64(number(value))
	if ms == 0 {
		return "从未签到"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

func number(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

// thousands 输出带千分位的整数。
func thousands(value float64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := fmt.Sprintf("%.0f", value)
	var b []byte
	for i, ch := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, ch)
	}
	if negative {
		return "-" + string(b)
	}
	return string(b)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
