package openai

import (
	"strings"
	"testing"
)

// TestChatIDDerivedFromFirstUserMessage 验证无显式会话 ID 时按首条 user 消息派生。
func TestChatIDDerivedFromFirstUserMessage(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "你是助手"},
		map[string]any{"role": "user", "content": "看下目录结构"},
		map[string]any{"role": "assistant", "content": "好的"},
		map[string]any{"role": "user", "content": "再看下 go.mod"},
	}

	first := ChatID("", messages)
	if !isHex32(first) {
		t.Fatalf("chat_id 必须是 32 位十六进制, 得到 %q", first)
	}

	// 历史增长（工具链追加消息）不应改变会话 ID。
	grown := append([]any{}, messages...)
	grown = append(grown, map[string]any{"role": "user", "content": "继续"})
	if again := ChatID("", grown); again != first {
		t.Errorf("历史增长后 chat_id 变了: %q -> %q", first, again)
	}
}

// TestChatIDExplicitHexIsKept 验证调用方给出 32 位十六进制时原样使用。
func TestChatIDExplicitHexIsKept(t *testing.T) {
	const id = "0123456789ABCDEF0123456789abcdef"
	if got := ChatID(id, nil); got != strings.ToLower(id) {
		t.Errorf("显式 chat_id 应被保留并转小写, 得到 %q", got)
	}
}

// TestChatIDHashesNonHexExplicit 验证非十六进制的显式 ID 被哈希成稳定形态。
func TestChatIDHashesNonHexExplicit(t *testing.T) {
	first := ChatID("my-session-1", nil)
	if !isHex32(first) {
		t.Fatalf("应哈希成 32 位十六进制, 得到 %q", first)
	}
	if again := ChatID("my-session-1", nil); again != first {
		t.Errorf("同一显式 ID 应得到同一结果: %q != %q", first, again)
	}
	if other := ChatID("my-session-2", nil); other == first {
		t.Error("不同显式 ID 不应得到相同结果")
	}
}

// TestChatIDFallsBackToRandom 验证没有 user 消息时仍返回合法 ID。
func TestChatIDFallsBackToRandom(t *testing.T) {
	id := ChatID("", []any{map[string]any{"role": "system", "content": "hi"}})
	if !isHex32(id) {
		t.Fatalf("应回退为随机 32 位十六进制, 得到 %q", id)
	}
}

func TestSessionIDIsStableAndDistinct(t *testing.T) {
	a := SessionID("abc")
	if !isHex32(a) {
		t.Fatalf("session_id 必须是 32 位十六进制, 得到 %q", a)
	}
	if a != SessionID("abc") {
		t.Error("同一 chat_id 应得到同一 session_id")
	}
	if a == SessionID("abd") {
		t.Error("不同 chat_id 应得到不同 session_id")
	}
}

func TestMessageTextFlattensParts(t *testing.T) {
	parts := []any{
		map[string]any{"type": "text", "text": "第一段"},
		map[string]any{"type": "text", "text": "第二段"},
	}
	if got := MessageText(parts); got != "第一段第二段" {
		t.Errorf("分片 content 拍平结果 = %q", got)
	}
	if got := MessageText("纯文本"); got != "纯文本" {
		t.Errorf("字符串 content = %q", got)
	}
	if got := MessageText(nil); got != "" {
		t.Errorf("nil content = %q", got)
	}
}
