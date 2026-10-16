package version

import (
	"strings"
	"testing"
)

func TestStringIncludesAllFields(t *testing.T) {
	// 保存并恢复，避免污染其他用例。
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origVersion, origCommit, origDate })

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-10-16T00:00:00Z"

	got := String()
	for _, want := range []string{"v1.2.3", "abc1234", "2026-10-16T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, 缺少 %q", got, want)
		}
	}
}

func TestShortFallsBackToDev(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = ""
	if got := Short(); got != "dev" {
		t.Errorf("空版本号时 Short() = %q, want dev", got)
	}

	Version = "v2.0.0"
	if got := Short(); got != "v2.0.0" {
		t.Errorf("Short() = %q", got)
	}
}

func TestUserAgent(t *testing.T) {
	origVersion, origCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = origVersion, origCommit })

	Version, Commit = "v1.0.0", "0123456789abcdef"
	got := UserAgent()
	if !strings.HasPrefix(got, "codearts2api/v1.0.0") {
		t.Errorf("UserAgent() = %q", got)
	}
	// commit 应被截断到 8 位。
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("commit 未截断: %q", got)
	}

	// 无 commit 时不应出现多余的斜杠段。
	Commit = "none"
	if got := UserAgent(); got != "codearts2api/v1.0.0" {
		t.Errorf("UserAgent() = %q, want codearts2api/v1.0.0", got)
	}
}
