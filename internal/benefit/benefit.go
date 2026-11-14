// Package benefit 实现 CodeArts 活动积分的每日领取。
//
// 只剩一套账本：活动积分，走 CodeArts snap 根路径：
//
//	GET  /v1/ops/delivery?channel=IDE  活动列表（1=每日签到，4=新用户礼包）
//	POST /v1/ops/claim                 领取
//	POST /v1/ops/confirm               确认
//
// 原先的「限时福利额度」（opengw 的 /api/v1/benefit/claim、user/tokens/balance、
// gateway/config）已于 2026-10 随官方活动一起下线，这三个接口现在一律返回
// 4000: invalid channel, group not found，因此相关代码整体移除。
//
// 配合 Windows 计划任务可实现每日自动签到，例如每天 09:05：
//
//	schtasks /create /tn CodeArtsBenefit /tr "codearts2api.exe benefit claim" /sc daily /st 09:05
package benefit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
)

// Run 执行 benefit 子命令，返回进程退出码。
func Run(cfg *config.Config, creds *signer.Credentials, action string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return runActions(ctx, NewCredit(creds, cfg.Endpoints), action)
}

// runActions 是各子命令的实际实现。
//
// 免费额度通道下线后，status / balance / models 已无对应接口，保留这三个
// 名字只为给出明确提示，而不是让脚本以为是网络故障。
func runActions(ctx context.Context, credit *Credit, action string) int {
	switch action {
	case "", "default", "claim", "credit":
		if err := doCredit(ctx, credit); err != nil {
			return fail(err)
		}
	case "status", "balance", "models":
		fmt.Printf("benefit %s 不可用: 上游免费额度通道已于 2026-10 下线（4000: invalid channel, group not found）\n", action)
		return 1
	default:
		fmt.Printf("未知的 benefit 子命令: %s（可用: claim / credit）\n", action)
		return 1
	}
	return 0
}

// doCredit 领取活动积分（每日签到 / 新手礼包）。
func doCredit(ctx context.Context, credit *Credit) error {
	if credit == nil {
		return errors.New("未配置活动积分客户端")
	}
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
	}
	return 0
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
