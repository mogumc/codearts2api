package openai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// chatIDHexLen 是上游要求的 chat_id 长度：UUID 去掉连字符后的 32 位十六进制。
const chatIDHexLen = 32

// ChatID 生成向上游声明的会话 ID。
//
// 上游 /api/v2/chat/completions 用 chat_id 标识会话：同一 chat_id 的请求共享
// prompt cache，并被当作 IDE 会话的续接；不带时每个请求都是一次全新的匿名调用。
// OpenAI 线格式本身没有会话 ID，因此按「首条 user 消息」派生——Agent 工具链
// 的历史会持续增长，但首条 user 消息不变，可作稳定锚点。
//
// explicit 来自 X-Codearts-Chat-Id 头或 body 的 conversation_id：已是 32 位
// 十六进制就原样使用（便于调用方自己管理会话），否则同样哈希成稳定 ID。
func ChatID(explicit string, messages []any) string {
	if id := normalizeChatID(explicit); id != "" {
		return id
	}
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); !strings.EqualFold(strings.TrimSpace(role), "user") {
			continue
		}
		text := strings.TrimSpace(MessageText(msg["content"]))
		if text == "" {
			continue
		}
		return shortHash(text)
	}
	return NewChatID()
}

// SessionID 由 chat_id 派生稳定的会话标识（用于 Session-Id 头与缓存亲和）。
func SessionID(chatID string) string {
	return shortHash("codearts-session:" + chatID)
}

// MessageText 把 content 拍平成纯文本，兼容 string 与分片数组两种线格式。
func MessageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				sb.WriteString(text)
			}
		}
		return sb.String()
	}
	return ""
}

// NewChatID 生成一个随机的 32 位十六进制会话 ID。
func NewChatID() string {
	buf := make([]byte, chatIDHexLen/2)
	if _, err := rand.Read(buf); err != nil {
		return shortHash("chat")
	}
	return hex.EncodeToString(buf)
}

// normalizeChatID 归一化调用方显式给出的会话标识。
func normalizeChatID(explicit string) string {
	value := strings.TrimSpace(explicit)
	if value == "" {
		return ""
	}
	if isHex32(value) {
		return strings.ToLower(value)
	}
	return shortHash(value)
}

// isHex32 判断是否为 32 位十六进制（上游对 chat_id 的硬性要求）。
func isHex32(value string) bool {
	if len(value) != chatIDHexLen {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}

// shortHash 取 sha256 的前 16 字节十六进制，正好 32 位。
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:chatIDHexLen/2])
}
