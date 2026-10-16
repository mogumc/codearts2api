package benefit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
)

// fakeClaimServer 统计签到调用次数，便于验证「每天只签一次」。
type fakeClaimServer struct {
	mu    sync.Mutex
	posts int
	gets  int
	// respond 可自定义响应，返回 false 时使用默认成功响应。
	respond func(w http.ResponseWriter, r *http.Request) bool
}

func (f *fakeClaimServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			f.mu.Lock()
			f.posts++
			f.mu.Unlock()
		} else {
			f.mu.Lock()
			f.gets++
			f.mu.Unlock()
		}
		if f.respond != nil && f.respond(w, r) {
			return
		}
		_, _ = io.WriteString(w, `{"error_code":"0000","result":{"create_time":1789539411000}}`)
	})
}

func (f *fakeClaimServer) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

// newAutoClaimer 构造指向假上游的自动签到器。
func newAutoClaimer(t *testing.T, fake *fakeClaimServer, statePath string) *AutoClaimer {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)

	client := New(signer.New("demo-ak", "demo-sk"), config.DefaultEndpoints())
	client.claimURL = server.URL + "/api/v1/benefit/claim"
	client.balanceURL = server.URL + "/api/v1/user/tokens/balance"
	client.configURL = server.URL + "/api/v1/gateway/config"

	return NewAutoClaimer(client, statePath, nil)
}

// waitFor 轮询等待条件成立，避免测试依赖固定 sleep。
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// waitIdle 等待后台签到任务彻底结束。
//
// claimAsync 在最后（写完状态文件之后）才把 inFlight 置回 false，因此
// inFlight 归位即代表这一次签到已完全收尾。用它替代「睡一会儿再断言」，
// 断言结果才是确定的，不会因 -race 拖慢而偶发失败。
func waitIdle(t *testing.T, claimer *AutoClaimer) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool {
		claimer.mu.Lock()
		defer claimer.mu.Unlock()
		return !claimer.inFlight
	}) {
		t.Fatal("后台签到任务未在预期时间内结束")
	}
}

// TestClaimOncePerDay 是核心用例：同一天多次调用只签到一次。
func TestClaimOncePerDay(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	// 模拟一天内 20 个请求。
	for i := 0; i < 20; i++ {
		claimer.MaybeClaim()
	}

	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() == 1 }) {
		t.Fatalf("签到次数 = %d, 期望 1", fake.postCount())
	}
	// 等后台任务彻底收尾后再断言，确认不会再多打。
	waitIdle(t, claimer)
	if got := fake.postCount(); got != 1 {
		t.Errorf("签到次数 = %d, 期望恰好 1 次", got)
	}
}

// TestMaybeClaimDoesNotBlock 验证请求路径上的判断不等待网络响应。
//
// 这里不比较墙钟耗时（-race 等场景下阈值不可靠），而是直接验证语义：
// 上游响应被挂住不放时，MaybeClaim 依然能立即返回。
func TestMaybeClaimDoesNotBlock(t *testing.T) {
	// release 控制上游响应何时返回；在此之前请求会一直挂着。
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return false
	}}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	done := make(chan struct{})
	go func() {
		defer close(done)
		claimer.MaybeClaim()
	}()

	// MaybeClaim 必须在上游仍挂起时就返回。
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MaybeClaim 被上游响应阻塞了")
	}

	// 确认上游确实已被调用且仍挂起（说明我们测的是真实的异步路径）。
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("上游请求未发出")
	}

	// 放行上游，让后台 goroutine 正常收尾。
	close(release)
	waitIdle(t, claimer)
}

// TestNoClaimWhenAlreadyClaimedToday 验证当天已签到后不再请求上游。
func TestNoClaimWhenAlreadyClaimedToday(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")

	// 预置「今天已签到」状态。
	seed := State{LastSuccessDate: time.Now().Format(dateLayout), LastClaimTime: 1789539411000}
	if err := seed.Save(statePath); err != nil {
		t.Fatal(err)
	}

	claimer := newAutoClaimer(t, fake, statePath)
	for i := 0; i < 10; i++ {
		claimer.MaybeClaim()
	}
	waitIdle(t, claimer)

	if got := fake.postCount(); got != 0 {
		t.Errorf("当天已签到却发起了 %d 次请求", got)
	}
}

// TestNewDayTriggersClaim 验证跨天后会重新签到。
func TestNewDayTriggersClaim(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")

	// 预置昨天的签到记录。
	seed := State{LastSuccessDate: time.Now().AddDate(0, 0, -1).Format(dateLayout)}
	if err := seed.Save(statePath); err != nil {
		t.Fatal(err)
	}

	claimer := newAutoClaimer(t, fake, statePath)
	claimer.MaybeClaim()

	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() == 1 }) {
		t.Fatalf("跨天应重新签到, 实际 %d 次", fake.postCount())
	}
}

// TestStatePersistedAcrossRestart 验证签到状态落盘，重启后不会重复签到。
func TestStatePersistedAcrossRestart(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")

	first := newAutoClaimer(t, fake, statePath)
	first.MaybeClaim()

	// 等到状态真正落盘（请求发出 ≠ 状态已保存）。
	if !waitFor(t, 3*time.Second, func() bool {
		raw, err := os.ReadFile(statePath)
		return err == nil && len(raw) > 0
	}) {
		t.Fatal("签到状态未落盘")
	}

	// 模拟进程重启：新建实例应读到已签到的日期。
	second := newAutoClaimer(t, fake, statePath)
	if !second.State().claimedOn(time.Now().Format(dateLayout)) {
		t.Fatal("重启后应保留今日已签到状态")
	}
	for i := 0; i < 5; i++ {
		second.MaybeClaim()
	}
	waitIdle(t, second)

	if got := fake.postCount(); got != 1 {
		t.Errorf("重启后不应重复签到, 总次数 = %d", got)
	}
}

// TestFailureBackoff 验证签到失败后按退避时间重试，而不是每个请求都打。
func TestFailureBackoff(t *testing.T) {
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error_msg":"service unavailable"}`)
		return true
	}}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	// 模拟同一天内大量请求。
	for i := 0; i < 30; i++ {
		claimer.MaybeClaim()
	}

	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() >= 1 }) {
		t.Fatal("应至少尝试一次签到")
	}
	waitIdle(t, claimer)

	if got := fake.postCount(); got != 1 {
		t.Errorf("失败退避失效，30 个请求发起 %d 次签到，期望 1 次", got)
	}
	if err := claimer.State().LastError; err == "" {
		t.Error("失败后应记录错误原因")
	}
}

// TestFailureRetriesAfterBackoff 验证退避时间过后会重试。
func TestFailureRetriesAfterBackoff(t *testing.T) {
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error_msg":"down"}`)
		return true
	}}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	// 用可控时钟模拟时间流逝。
	var nowMs atomic.Int64
	base := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	nowMs.Store(base.UnixMilli())
	claimer.now = func() time.Time { return time.UnixMilli(nowMs.Load()) }

	claimer.MaybeClaim()
	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() == 1 }) {
		t.Fatal("首次尝试未发生")
	}

	// 未过退避期：不应重试。
	claimer.MaybeClaim()
	waitIdle(t, claimer)
	if got := fake.postCount(); got != 1 {
		t.Errorf("退避期内不应重试, 次数 = %d", got)
	}

	// 越过退避期：应重试。
	nowMs.Store(base.Add(retryBackoff + time.Minute).UnixMilli())
	claimer.MaybeClaim()
	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() == 2 }) {
		t.Errorf("退避期过后应重试, 次数 = %d", fake.postCount())
	}
}

// TestAlreadyClaimedTreatedAsSuccess 验证「今日已领取」类业务错误按成功处理。
func TestAlreadyClaimedTreatedAsSuccess(t *testing.T) {
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		_, _ = io.WriteString(w, `{"error_code":"0001","error_msg":"today already claimed"}`)
		return true
	}}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	claimer.MaybeClaim()
	if !waitFor(t, 3*time.Second, func() bool {
		return claimer.State().claimedOn(time.Now().Format(dateLayout))
	}) {
		t.Fatal("「今日已领取」应被识别为已签到")
	}
	// 等待状态落盘，避免与后续断言竞态。
	if !waitFor(t, 3*time.Second, func() bool {
		raw, err := os.ReadFile(statePath)
		return err == nil && len(raw) > 0
	}) {
		t.Fatal("状态未落盘")
	}

	// 识别为成功后，当天不再重试。
	for i := 0; i < 5; i++ {
		claimer.MaybeClaim()
	}
	waitIdle(t, claimer)
	if got := fake.postCount(); got != 1 {
		t.Errorf("识别为已签到后不应重试, 次数 = %d", got)
	}
	if claimer.State().LastError != "" {
		t.Errorf("识别为成功后不应残留错误: %q", claimer.State().LastError)
	}
}

// TestIsAlreadyClaimed 覆盖各类「已领取」文案。
func TestIsAlreadyClaimed(t *testing.T) {
	positives := []string{
		"today already claimed",
		"Repeated claim",
		"duplicate request",
		"claimed",
		"already claimed",
		"今日已领取",
		"已经签到过了",
		"重复签到",
		"重复领取",
	}
	for _, message := range positives {
		if !isAlreadyClaimed(errString(message)) {
			t.Errorf("isAlreadyClaimed(%q) = false, 应为 true", message)
		}
	}

	// 否定语义：含 claimed/already 等正面词，但语义是「尚未领取/领取失败」。
	// 这批文案一旦被判成已签到，当天不会再重试，直接丢掉一天额度。
	negated := []string{
		"benefit not claimed",
		"quota unclaimed",
		"claim failed, nothing claimed",
		"error: not yet claimed today",
		"today's benefit is not claimed",
		"cannot claim now",
		"never claimed",
		"领取失败",
		"签到失败",
		"尚未领取",
		"没有签到",
		"无法领取",
	}
	for _, message := range negated {
		if isAlreadyClaimed(errString(message)) {
			t.Errorf("isAlreadyClaimed(%q) = true, 否定语义应判为 false", message)
		}
	}

	// 不含正面关键词的普通失败。
	plainFailures := []string{
		"service unavailable",
		"IAM service is currently unavailable",
		"network timeout",
		"",
	}
	for _, message := range plainFailures {
		if isAlreadyClaimed(errString(message)) {
			t.Errorf("isAlreadyClaimed(%q) = true, 应为 false", message)
		}
	}

	if isAlreadyClaimed(nil) {
		t.Error("nil 不应被判定为已领取")
	}
}

// TestConcurrentMaybeClaimTriggersOnlyOne 验证并发调用下只会签一次。
func TestConcurrentMaybeClaimTriggersOnlyOne(t *testing.T) {
	fake := &fakeClaimServer{respond: func(w http.ResponseWriter, r *http.Request) bool {
		time.Sleep(50 * time.Millisecond)
		return false
	}}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimer.MaybeClaim()
		}()
	}
	wg.Wait()

	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() == 1 }) {
		t.Fatalf("并发下签到次数 = %d, 期望 1", fake.postCount())
	}
	waitIdle(t, claimer)
	if got := fake.postCount(); got != 1 {
		t.Errorf("并发下签到次数 = %d, 期望恰好 1", got)
	}
}

// TestStateFileWrittenAtomically 验证状态文件内容合法且无临时文件残留。
func TestStateFileWrittenAtomically(t *testing.T) {
	fake := &fakeClaimServer{}
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	claimer := newAutoClaimer(t, fake, statePath)

	claimer.MaybeClaim()

	// 等到状态文件真正落盘（内存状态更新与落盘之间有一个很短的时间差）。
	if !waitFor(t, 3*time.Second, func() bool {
		raw, err := os.ReadFile(statePath)
		return err == nil && len(raw) > 0
	}) {
		t.Fatal("状态文件未落盘")
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("状态文件非法 JSON: %v", err)
	}
	if state.LastSuccessDate == "" {
		t.Error("状态文件缺少 LastSuccessDate")
	}
	if state.LastClaimTime == 0 {
		t.Error("状态文件应记录 create_time")
	}
	if _, err := os.Stat(statePath + ".tmp"); !os.IsNotExist(err) {
		t.Error("临时文件未清理")
	}
}

// TestLoadStateHandlesBrokenFile 验证状态文件损坏时不影响启动。
func TestLoadStateHandlesBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "benefit-state.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := LoadState(path)
	if state.LastSuccessDate != "" {
		t.Error("损坏文件应返回零值状态")
	}
	if state.claimedOn(time.Now().Format(dateLayout)) {
		t.Error("零值状态不应被判定为已签到")
	}
}

// TestDescribe 验证启动日志中的状态描述。
func TestDescribe(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "benefit-state.json")
	fake := &fakeClaimServer{}

	// 未签到。
	claimer := newAutoClaimer(t, fake, statePath)
	if got := claimer.Describe(); got == "" {
		t.Error("Describe 不应为空")
	}

	// 已签到。
	seed := State{LastSuccessDate: time.Now().Format(dateLayout), LastClaimTime: 1789539411000}
	if err := seed.Save(statePath); err != nil {
		t.Fatal(err)
	}
	claimed := newAutoClaimer(t, fake, statePath)
	if got := claimed.Describe(); got == "" {
		t.Error("Describe 不应为空")
	}
}

// errString 是仅带消息的 error 实现。
type errString string

func (e errString) Error() string { return string(e) }

// TestMaybeClaimWithNilStatePath 验证未配置状态路径时仍能工作（只是不持久化）。
func TestMaybeClaimWithNilStatePath(t *testing.T) {
	fake := &fakeClaimServer{}
	claimer := newAutoClaimer(t, fake, "")

	claimer.MaybeClaim()
	if !waitFor(t, 3*time.Second, func() bool { return fake.postCount() == 1 }) {
		t.Fatal("未配置状态路径时也应能签到")
	}
}

// TestStateSaveCreatesDirectory 验证状态文件目录会被自动创建。
func TestStateSaveCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "benefit-state.json")
	state := State{LastSuccessDate: "2026-10-16"}
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("状态文件未创建: %v", err)
	}
}
