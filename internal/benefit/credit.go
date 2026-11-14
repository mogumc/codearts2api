// 活动积分领取：每日签到与新用户礼包。
//
// 与「限时福利额度」是两套独立账本：
//   - 福利额度走 opengw.developer.huaweicloud.com 的 /api/v1/benefit/claim；
//   - 活动积分走 CodeArts snap 根路径的 /v1/ops/*，签名 host 也不同。
//
// 接口形状（AK/SK 签名 + Agent-Type: PromptCenter，注意不是 snap-manager 前缀）：
//
//	GET  /v1/ops/delivery?channel=IDE  活动列表，返回 {code:0,data:{items:[...]}}
//	POST /v1/ops/claim                 领取 {campaignId,channel,idempotentKey}
//	POST /v1/ops/confirm               确认 {campaignId}
//
// 上游决定资格与每日刷新节奏：已领取的不再 claim，CLAIMED 只补 confirm。
package benefit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
	"codearts2api/internal/upstream"
)

const (
	// campaignDaily 是每日签到活动；campaignNewUser 是新用户礼包。
	// 学生认证、邀请等其余活动不自动参与。
	campaignDaily   = 1
	campaignNewUser = 4

	// creditMaxBody 限制活动接口响应的读取量。
	creditMaxBody = 2 << 20
	// creditTruncate 是错误落日志时保留的响应长度。
	creditTruncate = 300
)

// Campaign 是活动投放接口的一条活动。
//
// 刻意只保留脱敏字段：delivery 响应里还带活动跳转票据，不该落盘或打日志。
type Campaign struct {
	ID            int     `json:"campaignId"`
	Title         string  `json:"title"`
	Status        string  `json:"status"`
	Claimable     bool    `json:"claimable"`
	BenefitUnit   string  `json:"benefitUnit"`
	BenefitAmount float64 `json:"benefitAmount"`
	// PendingTotalAmount 是本次待入账金额，领取成功后用它作为「本次领取量」。
	PendingTotalAmount float64 `json:"pendingTotalAmount"`
	Extra              struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"extra"`
}

// CampaignResult 是单个活动的领取结果。
type CampaignResult struct {
	CampaignID int     `json:"campaign_id"`
	Title      string  `json:"title"`
	OK         bool    `json:"ok"`
	Status     string  `json:"status"`
	Credits    float64 `json:"credits,omitempty"`
	Message    string  `json:"message"`
}

// CheckinResult 是一次签到的汇总；Credits 是本次领取量，不是账号余额。
type CheckinResult struct {
	OK        bool             `json:"ok"`
	Credits   float64          `json:"credits,omitempty"`
	Message   string           `json:"message"`
	Campaigns []CampaignResult `json:"campaigns"`
}

// Credit 是活动积分客户端。
type Credit struct {
	client *upstream.Client

	// 端点可在测试中替换。
	deliveryURL string
	claimURL    string
	confirmURL  string

	now func() time.Time
}

// NewCredit 构造活动积分客户端。
//
// 签名 host 取 CodeArts 通道：活动接口虽然不带 /snap-manager，但仍在
// snap-access 域名下，走的是同一个 AK/SK 签名。
func NewCredit(creds *signer.Credentials, endpoints config.Endpoints) *Credit {
	return &Credit{
		client:      upstream.New(creds, endpoints.CodeArtsHost()),
		deliveryURL: endpoints.CreditDelivery(),
		claimURL:    endpoints.CreditClaim(),
		confirmURL:  endpoints.CreditConfirm(),
		now:         time.Now,
	}
}

// Campaigns 只读查询活动列表。
func (c *Credit) Campaigns(ctx context.Context) ([]Campaign, error) {
	raw, err := c.call(ctx, http.MethodGet, c.deliveryURL, nil)
	if err != nil {
		return nil, err
	}
	var data struct {
		Items *[]Campaign `json:"items"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("活动列表解析失败: %w", err)
	}
	// items 缺失说明响应形状变了，不能当成「没有活动」静默通过。
	if data.Items == nil {
		return nil, fmt.Errorf("活动列表缺少 data.items")
	}
	return *data.Items, nil
}

// Checkin 领取每日签到与新用户礼包。
//
// 同一账号必须串行调用：自动签到本身是单飞的，CLI 也不会并发。
func (c *Credit) Checkin(ctx context.Context) (CheckinResult, error) {
	campaigns, err := c.Campaigns(ctx)
	if err != nil {
		return CheckinResult{Message: err.Error()}, err
	}

	out := CheckinResult{OK: true, Campaigns: make([]CampaignResult, 0, 2)}
	for _, campaign := range campaigns {
		if campaign.ID != campaignDaily && campaign.ID != campaignNewUser {
			continue
		}
		// 只处理积分活动；额度类活动由 benefit.Client 负责。
		if campaign.BenefitUnit != "" && campaign.BenefitUnit != "CREDIT" {
			continue
		}
		res := c.claimOne(ctx, campaign)
		out.Campaigns = append(out.Campaigns, res)
		out.OK = out.OK && res.OK
		out.Credits += res.Credits
	}

	parts := make([]string, 0, len(out.Campaigns))
	for _, res := range out.Campaigns {
		parts = append(parts, res.Title+": "+res.Message)
	}
	out.Message = strings.Join(parts, "; ")
	if len(parts) == 0 {
		out.Message = "无可领取的签到或新手活动"
	}
	return out, nil
}

// claimOne 按活动状态决定是否领取，并在领取后补确认。
func (c *Credit) claimOne(ctx context.Context, campaign Campaign) CampaignResult {
	res := CampaignResult{
		CampaignID: campaign.ID,
		Title:      campaign.Title,
		OK:         true,
		Status:     strings.TrimSpace(campaign.Status),
	}
	if res.Title == "" {
		res.Title = fmt.Sprintf("积分活动 %d", campaign.ID)
	}

	now := c.now()
	if end, err := time.Parse(time.RFC3339Nano, campaign.Extra.EndTime); err == nil && !now.Before(end) {
		res.Message = "活动已结束"
		return res
	}
	if start, err := time.Parse(time.RFC3339Nano, campaign.Extra.StartTime); err == nil && now.Before(start) {
		res.Message = "活动尚未开始"
		return res
	}

	switch res.Status {
	case "CONFIRMED", "CONSUMED":
		res.Message = "已领取"
		return res
	case "CLAIMED":
		// 已到账但未确认：只能补确认，重复领取会被上游拒。
	case "ELIGIBLE":
		if !campaign.Claimable {
			res.OK, res.Message = false, "暂不可领取，稍后重试"
			return res
		}
		if _, err := c.call(ctx, http.MethodPost, c.claimURL, map[string]any{
			"campaignId":    campaign.ID,
			"channel":       config.CreditChannel,
			"idempotentKey": fmt.Sprintf("claim_%d_%d", campaign.ID, now.UnixMilli()),
		}); err != nil {
			res.OK, res.Message = false, err.Error()
			return res
		}
		res.Status = "CLAIMED"
		res.Credits = campaign.PendingTotalAmount
		if res.Credits <= 0 {
			res.Credits = campaign.BenefitAmount
		}
	case "":
		res.Message = "未获得活动资格"
		return res
	default:
		res.OK, res.Message = false, "未知活动状态: "+res.Status
		return res
	}

	if _, err := c.call(ctx, http.MethodPost, c.confirmURL, map[string]any{"campaignId": campaign.ID}); err != nil {
		res.OK, res.Message = false, "已领取，确认失败，稍后重试: "+err.Error()
		return res
	}
	res.Status = "CONFIRMED"
	if res.Credits > 0 {
		res.Message = fmt.Sprintf("领取成功（%g 积分）", res.Credits)
	} else {
		res.Message = "已补确认"
	}
	return res
}

// call 发起一次活动接口请求，并把 {code,message,data} 信封拆开。
func (c *Credit) call(ctx context.Context, method, url string, body any) (json.RawMessage, error) {
	var raw []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}

	// 活动接口要求 Agent-Type；这几个头都参与签名，与官方客户端一致。
	headers := map[string]string{
		"content-type": "application/json",
		"agent-type":   "PromptCenter",
		"x-language":   "zh-cn",
	}
	resp, err := c.client.Do(ctx, method, url, raw, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, creditMaxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(payload), creditTruncate))
	}

	var envelope struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("活动接口响应解析失败: %w", err)
	}
	// HTTP 200 也可能是业务失败，必须看 code。
	if envelope.Code == nil {
		return nil, fmt.Errorf("活动接口响应缺少业务码: %s", truncate(string(payload), creditTruncate))
	}
	if *envelope.Code != 0 {
		return nil, fmt.Errorf("业务错误 %d: %s", *envelope.Code, envelope.Message)
	}
	return envelope.Data, nil
}
