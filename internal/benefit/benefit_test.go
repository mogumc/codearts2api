package benefit

import (
	"strings"
	"testing"

	"codearts2api/internal/config"
)

// TestRunActionsClaimsCredit 验证默认与 claim/credit 子命令都走活动积分。
func TestRunActionsClaimsCredit(t *testing.T) {
	for _, action := range []string{"", "default", "claim", "credit"} {
		rec := &creditRecorder{deliveryBody: deliveryJSON(
			`{"campaignId":1,"title":"每日签到","status":"ELIGIBLE","claimable":true,"benefitUnit":"CREDIT","benefitAmount":5}`)}
		if code := runActions(t.Context(), newTestCredit(t, rec), action); code != 0 {
			t.Errorf("action %q 退出码 = %d, 期望 0", action, code)
		}
		if len(rec.claims) != 1 {
			t.Errorf("action %q 应领取一次, 实际 %d", action, len(rec.claims))
		}
	}
}

// TestRunActionsRejectsRetiredCommands 验证已下线接口对应的子命令给出明确提示。
func TestRunActionsRejectsRetiredCommands(t *testing.T) {
	for _, action := range []string{"status", "balance", "models"} {
		if code := runActions(t.Context(), nil, action); code != 1 {
			t.Errorf("action %q 退出码 = %d, 期望 1", action, code)
		}
	}
}

// TestRunActionsRejectsUnknownCommand 验证未知子命令不会静默成功。
func TestRunActionsRejectsUnknownCommand(t *testing.T) {
	if code := runActions(t.Context(), nil, "unknown"); code != 1 {
		t.Errorf("退出码 = %d, 期望 1", code)
	}
}

// TestRunActionsWithoutCredit 验证未配置客户端时返回失败而不是 panic。
func TestRunActionsWithoutCredit(t *testing.T) {
	if code := runActions(t.Context(), nil, "claim"); code != 1 {
		t.Errorf("退出码 = %d, 期望 1", code)
	}
}

// TestCreditChannelIsStable 验证活动接口的渠道标识没有随手改掉。
func TestCreditChannelIsStable(t *testing.T) {
	if config.CreditChannel != "IDE" {
		t.Errorf("CreditChannel = %q", config.CreditChannel)
	}
}

// TestFormatMillis 验证毫秒时间戳格式化。
func TestFormatMillis(t *testing.T) {
	if got := formatMillis(float64(0)); got != "从未签到" {
		t.Errorf("零值 = %q", got)
	}
	if got := formatMillis(nil); got != "从未签到" {
		t.Errorf("nil = %q", got)
	}
	if got := formatMillis(int64(1789539411000)); !strings.Contains(got, "-") {
		t.Errorf("时间格式异常: %q", got)
	}
}

// TestTruncate 验证超长内容被截断。
func TestTruncate(t *testing.T) {
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("短内容不应截断: %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("截断结果 = %q", got)
	}
}
