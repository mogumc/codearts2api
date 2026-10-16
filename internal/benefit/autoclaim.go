package benefit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 失败退避：签到失败后至少间隔这么久才会再次尝试，避免上游异常时
// 每个请求都去打一次签到接口。
const retryBackoff = 10 * time.Minute

// claimTimeout 是后台签到的超时时间。
const claimTimeout = 30 * time.Second

// AutoClaimer 实现「每天首次收到请求时顺带签一次」。
//
// 设计目标是让请求路径上的开销降到最低：
//   - 判断只需一次加锁读取内存中的日期字符串，不触网、不读盘；
//   - 真正的签到在网络请求的 goroutine 里异步执行，绝不阻塞用户请求；
//   - 每天最多成功签到一次，失败后按 retryBackoff 退避。
type AutoClaimer struct {
	client    *Client
	statePath string

	mu       sync.Mutex
	state    State
	inFlight bool
	now      func() time.Time // 可在测试中替换

	logf func(format string, args ...any)
}

// NewAutoClaimer 构造自动签到器，并从磁盘恢复上次的签到状态。
func NewAutoClaimer(client *Client, statePath string, logf func(string, ...any)) *AutoClaimer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &AutoClaimer{
		client:    client,
		statePath: statePath,
		state:     LoadState(statePath),
		now:       time.Now,
		logf:      logf,
	}
}

// State 返回当前签到状态快照。
func (a *AutoClaimer) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// MaybeClaim 在每次代理请求时调用。
//
// 它在请求路径上只做一次内存判断；需要签到时立刻返回，把网络请求交给后台
// goroutine，因此不会给用户请求增加可感知的延迟。
func (a *AutoClaimer) MaybeClaim() {
	if !a.shouldClaim() {
		return
	}
	go a.claimAsync()
}

// shouldClaim 判断当前是否需要发起签到（纯内存判断，不触网不读盘）。
func (a *AutoClaimer) shouldClaim() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.inFlight {
		return false
	}
	today := a.now().Format(dateLayout)
	if a.state.claimedOn(today) {
		return false
	}
	// 失败退避：距上次尝试不足 retryBackoff 时先不重试。
	if a.state.LastAttemptAt > 0 {
		elapsed := a.now().UnixMilli() - a.state.LastAttemptAt
		if elapsed >= 0 && elapsed < retryBackoff.Milliseconds() {
			return false
		}
	}
	a.inFlight = true
	return true
}

// claimAsync 执行一次后台签到并落盘状态。
func (a *AutoClaimer) claimAsync() {
	defer func() {
		a.mu.Lock()
		a.inFlight = false
		a.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), claimTimeout)
	defer cancel()

	info, err := a.client.Claim(ctx)

	a.mu.Lock()
	a.state.LastAttemptAt = a.now().UnixMilli()
	if err != nil {
		// 上游提示「今日已领取」时按成功处理，避免反复重试。
		if isAlreadyClaimed(err) {
			a.state.LastSuccessDate = a.now().Format(dateLayout)
			a.state.LastError = ""
		} else {
			a.state.LastError = err.Error()
		}
	} else {
		a.state.LastSuccessDate = a.now().Format(dateLayout)
		a.state.LastError = ""
		if ms, ok := info["create_time"]; ok {
			a.state.LastClaimTime = int64(number(ms))
		}
	}
	snapshot := a.state
	a.mu.Unlock()

	if err := snapshot.Save(a.statePath); err != nil {
		a.logf("自动签到状态写入失败: %v", err)
	}

	if err != nil && !isAlreadyClaimed(err) {
		a.logf("自动签到失败（%v 后重试）: %v", retryBackoff, err)
		return
	}
	a.logf("自动签到成功，签到时间: %s", formatMillis(snapshot.LastClaimTime))
}

// isAlreadyClaimed 识别「今日已领取」类业务错误。
//
// 上游对同一天重复签到返回业务错误；这不是真正的失败，按成功处理可以
// 避免当天反复重试，也不会把状态标记成异常。
//
// 判断刻意保持保守：误判的代价是把「真实失败」当成已签到，从而丢掉一天的
// 额度。所以先排除否定语义，再要求出现「已领取」关键词。
//
// 必须排除否定语义的原因：claimed 会命中 "not claimed" / "unclaimed" /
// "nothing claimed" 这类「尚未领取」的表述，把它们当成已签到会让当天不再
// 重试，直接丢掉一天额度。
func isAlreadyClaimed(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())

	if hasNegatedClaim(message) {
		return false
	}

	for _, keyword := range []string{"already", "repeated", "duplicate", "claimed"} {
		if strings.Contains(message, keyword) {
			return true
		}
	}

	// 中文不枚举具体文案，要求同时出现「动作」与「已完成/重复」两种语义——
	// 「已签到」「已经签到过了」「重复领取」都能命中，「签到失败」「领取超时」不会。
	hasAction := strings.Contains(message, "签到") || strings.Contains(message, "领取")
	hasDone := strings.Contains(message, "已") || strings.Contains(message, "重复")
	return hasAction && hasDone
}

// hasNegatedClaim 判断消息是否表达「尚未/未能领取」。
//
// 只在消息含正面关键词时才可能走到这里，因此这里只需拦住「含正面词但语义
// 是否定」的表述。不收录泛化的 fail/error —— 那些消息不含 claimed/already
// 等正面词，本来就返回 false；收进来反而会误伤 "claim failed: already claimed"
// 这类把已领取包在失败描述里的文案。
func hasNegatedClaim(message string) bool {
	for _, keyword := range []string{
		// 英文否定
		"not ", "not_", "cannot", "never", "unclaimed", "no claim", "nothing claimed",
		// 中文否定：「已经领取失败」含「已」和「领取」，必须靠否定词拦住
		"未", "没", "失败", "无法", "不可",
	} {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}

// Describe 返回签到状态的人类可读描述，供启动日志使用。
func (a *AutoClaimer) Describe() string {
	state := a.State()
	today := time.Now().Format(dateLayout)
	if state.claimedOn(today) {
		return fmt.Sprintf("今日已签到（%s）", formatMillis(state.LastClaimTime))
	}
	if state.LastError != "" {
		return fmt.Sprintf("今日尚未签到，上次失败: %s", state.LastError)
	}
	return "今日尚未签到，将在首个请求时自动签到"
}
