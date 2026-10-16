package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// sseReader 按行读取上游 SSE，单行长度上限放宽到 16MB 以容纳大 chunk。
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &sseReader{scanner: scanner}
}

// readLine 返回下一行；到达末尾时返回 io.EOF。
func (r *sseReader) readLine() (*string, error) {
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	line := r.scanner.Text()
	return &line, nil
}

// writeSSE 写入一个 data 帧。
func writeSSE(w io.Writer, payload []byte) {
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
}

// writeSSEDone 写入流结束标记。
func writeSSEDone(w io.Writer) {
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func flush(f http.Flusher) {
	if f != nil {
		f.Flush()
	}
}

// marshalCompact 生成与 Python json.dumps(separators=(",", ":")) 等价的紧凑 JSON。
// 这里显式转义 HTML 字符，保证与 Python 的 ensure_ascii=False 输出一致。
func marshalCompact(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// writeJSON 输出带缩进无关的紧凑 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	writeRawJSON(w, status, value)
}

// writeRawJSON 直接序列化任意值写出。
func writeRawJSON(w http.ResponseWriter, status int, value any) {
	payload, err := marshalCompact(value)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"encode error"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// writeError 输出 OpenAI 风格错误响应。
func writeError(w http.ResponseWriter, status int, message, errType, code string) {
	body := map[string]any{"message": message}
	if errType != "" {
		body["type"] = errType
	}
	if code != "" {
		body["code"] = code
	}
	writeRawJSON(w, status, map[string]any{"error": body})
}
