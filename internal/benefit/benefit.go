// Package benefit 实现 CodeArts 的每日领取与查询。
//
// 两套独立账本：
//
//  1. 活动积分（credit.go）—— 当前有效的「签到」入口，走 CodeArts snap 根路径：
//     GET  /v1/ops/delivery?channel=IDE  活动列表（1=每日签到，4=新用户礼包）
//     POST /v1/ops/claim                 领取
//     POST /v1/ops/confirm               确认
//
//  2. 限时福利额度（benefit.go）—— 免费模型额度，走 opengw：
//     GET  /api/v1/benefit/claim        签到状态（返回上次签到时间）
//     POST /api/v1/benefit/claim        领取当日免费额度（空 JSON 对象）
//     GET  /api/v1/user/tokens/balance  查询免费额度余额
//     GET  /api/v1/gateway/config       免费模型列表
//
// 配合 Windows 计划任务可实现每日自动签到，例如每天 09:05：
//
//	schtasks /create /tn CodeArtsBenefit /tr "codearts2api.exe benefit claim" /sc daily /st 09:05
package benefit

import (
	"context"
	"encoding/json"
	"errors"
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
	// POST 显式送一个空 JSON 对象：上游对零长度 body 的 POST 不友好，
	// 官方客户端也是这么发的。
	var body []byte
	if method == http.MethodPost {
		// POST 显式送一个空 JSON 对象：上游对零长度 body 的 POST 不友好，
		// 官方客户端也是这么发的。
		body = []byte("{}")
	}
	headers := map[string]string{"x-language": "zh-cn"}

	resp, err := c.client.Do(ctx, method, url, body, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(payload), 300))
	}

	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	if !isSuccessCode(parsed["error_code"]) {
		return nil, fmt.Errorf("业务错误 %v: %v", parsed["error_code"], parsed["error_msg"])
	}
	result, _ := parsed["result"].(map[string]any)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return runActions(ctx, New(creds, cfg.Endpoints), NewCredit(creds, cfg.Endpoints), action)
}

// runActions 是各子命令的实际实现；credit 可为 nil（测试或未接入积分时）。
func runActions(ctx context.Context, client *Client, credit *Credit, action string) int {
	switch action {
	case "status":
		if err := showStatus(ctx, client); err != nil {
			return fail(err)
		}
	case "claim":
		if err := claimAll(ctx, client, credit); err != nil {
			return fail(err)
		}
		// 余额查询失败不影响签到结论（流水账本可能单独故障）。
		if err := showBalance(ctx, client); err != nil {
			fmt.Println("余额查询失败:", err)
		}
	case "credit":
		if credit == nil {
			return fail(errors.New("未配置活动积分客户端"))
		}
		if err := doCredit(ctx, credit); err != nil {
			return fail(err)
		}
	case "models":
		fmt.Println("免费模型列表:")
		if err := showModels(ctx, client); err != nil {
			return fail(err)
		}
	default:
		// 默认：状态 + 签到 + 余额。只有签到是硬性要求，
		// 状态/余额接口挂了不该把整条命令判失败。
		if err := showStatus(ctx, client); err != nil {
			fmt.Println("额度状态查询失败:", err)
		}
		if err := claimAll(ctx, client, credit); err != nil {
			return fail(err)
		}
		if err := showBalance(ctx, client); err != nil {
			fmt.Println("余额查询失败:", err)
		}
	}
	return 0
}

// claimAll 同时领取活动积分与免费额度，只要有一边成功就算签到成功。
//
// 两套账本都可能单独故障：积分接口停了不该连带否定额度，反之亦然。
func claimAll(ctx context.Context, client *Client, credit *Credit) error {
	var creditErr, benefitErr error
	if credit != nil {
		creditErr = doCredit(ctx, credit)
	}
	benefitErr = doClaim(ctx, client)

	switch {
	case creditErr == nil && benefitErr == nil:
		return nil
	case creditErr == nil:
		fmt.Println("免费额度签到失败（不影响活动积分）:", benefitErr)
		return nil
	case benefitErr == nil:
		fmt.Println("活动积分领取失败:", creditErr)
		return nil
	}
	return fmt.Errorf("活动积分: %v；免费额度: %v", creditErr, benefitErr)
}

// doCredit 领取活动积分（每日签到 / 新手礼包）。
func doCredit(ctx context.Context, credit *Credit) error {
	result, err := credit.Checkin(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("活动积分: %s\n", result.Message)
	if !result.OK {
		return errors.New(result.Message)
	}
	return nil
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
