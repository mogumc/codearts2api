package benefit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
)

// creditRecorder 记录活动接口的调用，便于断言「谁被领取了」。
type creditRecorder struct {
	mu       sync.Mutex
	delivery int
	claims   []map[string]any
	confirms []map[string]any

	// deliveryBody 可自定义投放列表响应。
	deliveryBody string
	// claimStatus 非 200 时用于模拟网关错误。
	gatewayStatus int
}

func (r *creditRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.gatewayStatus >= 400 {
			w.WriteHeader(r.gatewayStatus)
			_, _ = io.WriteString(w, `{"error_code":"APIG.0301","error_msg":"signature invalid"}`)
			return
		}
		switch req.URL.Path {
		case "/v1/ops/delivery":
			r.mu.Lock()
			r.delivery++
			body := r.deliveryBody
			r.mu.Unlock()
			if req.URL.Query().Get("channel") != config.CreditChannel {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":400,"message":"channel required"}`)
				return
			}
			_, _ = io.WriteString(w, body)
		case "/v1/ops/claim":
			r.mu.Lock()
			r.claims = append(r.claims, decodeBody(req))
			r.mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0,"message":"ok","data":{}}`)
		case "/v1/ops/confirm":
			r.mu.Lock()
			r.confirms = append(r.confirms, decodeBody(req))
			r.mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0,"message":"ok"}`)
		default:
			http.NotFound(w, req)
		}
	})
}

func decodeBody(r *http.Request) map[string]any {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return body
}

// newTestCredit 构造指向假上游的活动积分客户端。
func newTestCredit(t *testing.T, rec *creditRecorder) *Credit {
	t.Helper()
	server := httptest.NewServer(rec.handler())
	t.Cleanup(server.Close)

	credit := NewCredit(signer.New("demo-ak", "demo-sk"), config.DefaultEndpoints())
	credit.deliveryURL = server.URL + "/v1/ops/delivery?channel=" + config.CreditChannel
	credit.claimURL = server.URL + "/v1/ops/claim"
	credit.confirmURL = server.URL + "/v1/ops/confirm"
	return credit
}

// deliveryJSON 拼一个活动列表响应；items 是已序列化的活动对象字面量。
func deliveryJSON(items string) string {
	return `{"code":0,"message":"ok","data":{"items":[` + items + `]}}`
}

// TestCreditCheckinClaimsEligibleOnly 验证只领 1/4 号活动，且 ELIGIBLE 会 claim + confirm。
func TestCreditCheckinClaimsEligibleOnly(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(strings.Join([]string{
		`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":true,"benefitUnit":"CREDIT","pendingTotalAmount":10,"extra":{"startTime":"2026-01-01T00:00:00Z","endTime":"2027-01-01T00:00:00Z"}}`,
		`{"campaignId":4,"title":"新用户礼包","status":"CONFIRMED","benefitUnit":"CREDIT"}`,
		// 其它活动一律不碰。
		`{"campaignId":9,"title":"学生认证","status":"ELIGIBLE","claimable":true,"benefitUnit":"CREDIT"}`,
	}, ","))}
	credit := newTestCredit(t, rec)

	result, err := credit.Checkin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("整体应成功: %+v", result)
	}
	if result.Credits != 10 {
		t.Errorf("本次领取量 = %v, want 10", result.Credits)
	}
	if len(rec.claims) != 1 {
		t.Fatalf("只应领取 1 个活动, 实际 %d", len(rec.claims))
	}
	claim := rec.claims[0]
	if claim["campaignId"] != float64(1) {
		t.Errorf("领取的活动 = %v", claim["campaignId"])
	}
	if claim["channel"] != config.CreditChannel {
		t.Errorf("channel = %v", claim["channel"])
	}
	key, _ := claim["idempotentKey"].(string)
	if !strings.HasPrefix(key, "claim_1_") {
		t.Errorf("idempotentKey = %q", key)
	}
	if len(rec.confirms) != 1 || rec.confirms[0]["campaignId"] != float64(1) {
		t.Errorf("领取后应补确认: %v", rec.confirms)
	}
}

// TestCreditCheckinConfirmsClaimedWithoutReclaim 验证 CLAIMED 只补确认，不重复领取。
func TestCreditCheckinConfirmsClaimedWithoutReclaim(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"CLAIMED","benefitUnit":"CREDIT"}`)}
	credit := newTestCredit(t, rec)

	if _, err := credit.Checkin(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(rec.claims) != 0 {
		t.Errorf("CLAIMED 不应重复领取: %v", rec.claims)
	}
	if len(rec.confirms) != 1 {
		t.Fatalf("应补一次确认, 实际 %d", len(rec.confirms))
	}
}

// TestCreditCheckinSkipsConfirmed 验证已确认的活动不发任何写请求。
func TestCreditCheckinSkipsConfirmed(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"CONFIRMED","benefitUnit":"CREDIT"},` +
			`{"campaignId":4,"title":"新用户礼包","status":"CONSUMED","benefitUnit":"CREDIT"}`)}
	credit := newTestCredit(t, rec)

	result, err := credit.Checkin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("已领取不算失败: %+v", result)
	}
	if len(rec.claims) != 0 || len(rec.confirms) != 0 {
		t.Errorf("已领取不应再写: claims=%v confirms=%v", rec.claims, rec.confirms)
	}
}

// TestCreditCheckinNotClaimable 验证 ELIGIBLE 但 claimable=false 时不领取。
func TestCreditCheckinNotClaimable(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":false}`)}
	credit := newTestCredit(t, rec)

	result, err := credit.Checkin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Error("不可领取时应标记为未成功")
	}
	if len(rec.claims) != 0 {
		t.Errorf("不应发起领取: %v", rec.claims)
	}
}

// TestCreditCheckinNoQualification 验证空状态视为没有资格，而不是失败。
func TestCreditCheckinNoQualification(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":""}`)}
	credit := newTestCredit(t, rec)

	result, err := credit.Checkin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("没有资格不算失败: %+v", result)
	}
	if !strings.Contains(result.Message, "未获得活动资格") {
		t.Errorf("message = %q", result.Message)
	}
}

// TestCreditCheckinExpiredCampaign 验证过期活动直接跳过。
func TestCreditCheckinExpiredCampaign(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":true,` +
			`"extra":{"endTime":"2020-01-01T00:00:00Z"}}`)}
	credit := newTestCredit(t, rec)

	result, err := credit.Checkin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.claims) != 0 {
		t.Errorf("过期活动不应领取: %v", rec.claims)
	}
	if !strings.Contains(result.Message, "已结束") {
		t.Errorf("message = %q", result.Message)
	}
}

// TestCreditRejectsBusinessError 验证 HTTP 200 + 业务码非 0 也算失败。
func TestCreditRejectsBusinessError(t *testing.T) {
	rec := &creditRecorder{deliveryBody: `{"code":500,"message":"系统繁忙"}`}
	credit := newTestCredit(t, rec)

	if _, err := credit.Checkin(t.Context()); err == nil {
		t.Fatal("业务码非 0 应返回错误")
	} else if !strings.Contains(err.Error(), "系统繁忙") {
		t.Errorf("应带上游文案: %v", err)
	}
}

// TestCreditRejectsMissingItems 验证响应形状变了不会被当成「没有活动」。
func TestCreditRejectsMissingItems(t *testing.T) {
	rec := &creditRecorder{deliveryBody: `{"code":0,"message":"ok","data":{}}`}
	credit := newTestCredit(t, rec)

	if _, err := credit.Checkin(t.Context()); err == nil {
		t.Fatal("缺少 data.items 应返回错误")
	}
}

// TestCreditRejectsGatewayError 验证非 200 直接失败。
func TestCreditRejectsGatewayError(t *testing.T) {
	rec := &creditRecorder{gatewayStatus: http.StatusUnauthorized}
	credit := newTestCredit(t, rec)

	if _, err := credit.Checkin(t.Context()); err == nil {
		t.Fatal("401 应返回错误")
	} else if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误应包含状态码: %v", err)
	}
}

// TestCreditSendsPromptCenterHeaders 验证活动接口的头与签名。
func TestCreditSendsPromptCenterHeaders(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = io.WriteString(w, deliveryJSON(""))
	}))
	defer server.Close()

	credit := NewCredit(signer.New("demo-ak", "demo-sk"), config.DefaultEndpoints())
	credit.deliveryURL = server.URL + "/v1/ops/delivery?channel=" + config.CreditChannel
	if _, err := credit.Campaigns(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := seen.Get("Agent-Type"); got != "PromptCenter" {
		t.Errorf("Agent-Type = %q", got)
	}
	if got := seen.Get("X-Language"); got != "zh-cn" {
		t.Errorf("X-Language = %q", got)
	}
	auth := seen.Get("Authorization")
	if !strings.Contains(auth, "agent-type") {
		t.Errorf("Agent-Type 必须参与签名: %s", auth)
	}
}

// TestAutoClaimerPrefersCreditOverBenefit 验证积分成功时，老额度接口失败只算提示。
func TestAutoClaimerPrefersCreditOverBenefit(t *testing.T) {
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error_code":"500","error_msg":"benefit offline"}`)
		return true
	}}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":true,"benefitUnit":"CREDIT","benefitAmount":5}`)}
	claimer.SetCredit(newTestCredit(t, rec))

	claimer.MaybeClaim()
	waitIdle(t, claimer)

	state := claimer.State()
	if state.LastSuccessDate != time.Now().Format(dateLayout) {
		t.Errorf("积分成功应记为已签到, state = %+v", state)
	}
	if state.LastError != "" {
		t.Errorf("不应残留失败: %q", state.LastError)
	}
	if !strings.Contains(state.LastWarning, "免费额度") {
		t.Errorf("应记录额度失败的提示: %q", state.LastWarning)
	}
	if state.LastClaimTime == 0 {
		t.Error("应记录签到完成时间")
	}
}

// TestAutoClaimerCreditFailureKeepsRetrying 验证积分失败时按失败处理，不当成已签到。
func TestAutoClaimerCreditFailureKeepsRetrying(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	rec := &creditRecorder{deliveryBody: `{"code":403,"message":"活动接口不可用"}`}
	claimer.SetCredit(newTestCredit(t, rec))

	claimer.MaybeClaim()
	waitIdle(t, claimer)

	state := claimer.State()
	if state.LastSuccessDate != "" {
		t.Errorf("失败不应记为已签到: %+v", state)
	}
	if !strings.Contains(state.LastError, "活动接口不可用") {
		t.Errorf("LastError = %q", state.LastError)
	}
}

// TestAutoClaimerWithoutCreditKeepsLegacyBehavior 验证未挂积分时回到旧语义。
func TestAutoClaimerWithoutCreditKeepsLegacyBehavior(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	claimer.MaybeClaim()
	waitIdle(t, claimer)

	state := claimer.State()
	if state.LastSuccessDate != time.Now().Format(dateLayout) {
		t.Errorf("旧路径成功应记为已签到: %+v", state)
	}
	if state.LastWarning != "" {
		t.Errorf("旧路径不应有提示: %q", state.LastWarning)
	}
}

// TestRunActionsCredit 验证 credit 子命令只领活动积分。
func TestRunActionsCredit(t *testing.T) {
	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":true,"benefitUnit":"CREDIT","benefitAmount":5}`)}
	credit := newTestCredit(t, rec)

	if code := runActions(t.Context(), nil, credit, "credit"); code != 0 {
		t.Errorf("credit 子命令退出码 = %d, 期望 0", code)
	}
	if len(rec.claims) != 1 {
		t.Errorf("应领取一次, 实际 %d", len(rec.claims))
	}
}

// TestClaimAllToleratesOneLedgerFailure 验证两套账本只要一边成功就算成功。
func TestClaimAllToleratesOneLedgerFailure(t *testing.T) {
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error_code":"500","error_msg":"benefit offline"}`)
		return true
	}}
	claimer := newAutoClaimer(t, fake, filepath.Join(t.TempDir(), "benefit-state.json"))

	rec := &creditRecorder{deliveryBody: deliveryJSON(
		`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":true,"benefitUnit":"CREDIT"}`)}
	credit := newTestCredit(t, rec)

	// 额度接口挂了，但积分领到了 —— 整体不算失败。
	if err := claimAll(t.Context(), claimer.client, credit); err != nil {
		t.Errorf("一边成功不应返回错误: %v", err)
	}

	// 两边都挂才失败。
	rec2 := &creditRecorder{deliveryBody: `{"code":500,"message":"积分接口挂了"}`}
	credit2 := newTestCredit(t, rec2)
	if err := claimAll(t.Context(), claimer.client, credit2); err == nil {
		t.Error("两边都失败应返回错误")
	}
}
