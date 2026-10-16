package signer

import (
	"net/url"
	"testing"
	"time"
)

const (
	testAK = "demo-ak-not-real"
	testSK = "demo-sk-not-real"
)

func TestPercentEncode(t *testing.T) {
	cases := map[string]string{
		"abcXYZ019":  "abcXYZ019",
		"-_.~":       "-_.~",
		"a b":        "a%20b",
		"a/b":        "a%2Fb",
		"中":          "%E4%B8%AD",
		"a=b&c":      "a%3Db%26c",
		"plus+sign":  "plus%2Bsign",
		"percent%20": "percent%2520",
	}
	for input, want := range cases {
		if got := PercentEncode(input); got != want {
			t.Errorf("PercentEncode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCanonicalQuery(t *testing.T) {
	// 查询参数需按 (key, value) 排序后重新编码。
	got := CanonicalQuery("limit=100&offset=0")
	want := "limit=100&offset=0"
	if got != want {
		t.Errorf("CanonicalQuery = %q, want %q", got, want)
	}

	got = CanonicalQuery("b=2&a=1&a=0")
	want = "a=0&a=1&b=2"
	if got != want {
		t.Errorf("CanonicalQuery = %q, want %q", got, want)
	}

	if got := CanonicalQuery(""); got != "" {
		t.Errorf("CanonicalQuery(empty) = %q, want empty", got)
	}
}

func TestCanonicalURITrailingSlash(t *testing.T) {
	parsed, err := url.Parse("https://example.com/api/v2/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	request := CanonicalRequest("POST", parsed, map[string]string{"host": parsed.Host}, []byte("{}"))
	if request[:5] != "POST\n" {
		t.Fatalf("CanonicalRequest 未以方法开头: %q", request)
	}
	lines := splitLines(request)
	if lines[1] != "/api/v2/chat/completions/" {
		t.Errorf("CanonicalURI = %q, want 带尾斜杠", lines[1])
	}
}

func TestSignedHeaderNamesSortedAndLowercase(t *testing.T) {
	headers := map[string]string{
		"Host":         "example.com",
		"X-SDK-Date":   "20260101T000000Z",
		"Content-Type": "application/json",
		"maas_type":    "benefit",
	}
	got := SignedHeaderNames(headers)
	want := "content-type;host;maas_type;x-sdk-date"
	if got != want {
		t.Errorf("SignedHeaderNames = %q, want %q", got, want)
	}
}

func TestSignedHeaderNamesExcludesAuthorization(t *testing.T) {
	headers := map[string]string{
		"host":          "example.com",
		"Authorization": "SDK-HMAC-SHA256 Access=xxx",
	}
	got := SignedHeaderNames(headers)
	if got != "host" {
		t.Errorf("SignedHeaderNames = %q, want %q", got, "host")
	}
}

func TestSignDeterministic(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	body := []byte(`{"model":"openpangu-2.0-pro"}`)
	url := "https://snap-access.cn-north-4.myhuaweicloud.com/api/v2/chat/completions"

	first := creds.SignAt(now, "POST", url, "", body, nil)
	second := creds.SignAt(now, "POST", url, "", body, nil)

	if first["Authorization"] != second["Authorization"] {
		t.Fatalf("相同输入应产生相同签名:\n%s\n%s", first["Authorization"], second["Authorization"])
	}
	if first["x-sdk-date"] != "20260102T030405Z" {
		t.Errorf("x-sdk-date = %q", first["x-sdk-date"])
	}
	if first["host"] != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("host = %q", first["host"])
	}
}

func TestSignBenefitHeadersAffectSignature(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	body := []byte(`{"model":"glm-5.3-flash"}`)
	url := "https://snap-access.cn-north-4.myhuaweicloud.com/api/v2/chat/completions"

	plain := creds.SignAt(now, "POST", url, "", body, nil)
	benefit := creds.SignAt(now, "POST", url, "", body, map[string]string{
		"maas_type":  "benefit",
		"model-id":   "glm-5.3-flash",
		"model-name": "glm-5.3-flash",
	})

	if plain["Authorization"] == benefit["Authorization"] {
		t.Fatal("benefit 通道附加头必须参与签名，两次签名不应相同")
	}
	if !contains(benefit["Authorization"], "maas_type") {
		t.Errorf("SignedHeaders 应包含 maas_type: %q", benefit["Authorization"])
	}
}

// TestSignMatchesPythonReference 用固定输入锁定签名算法，防止回归。
// 期望值由 Python 参考实现（server.py 的 _hmac_headers）以相同 AK/SK 与时间戳算出。
func TestSignMatchesPythonReference(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	body := []byte(`{"model":"glm-5.3-flash"}`)
	url := "https://snap-access.cn-north-4.myhuaweicloud.com/api/v2/chat/completions"

	headers := creds.SignAt(now, "POST", url, "", body, map[string]string{
		"maas_type":  "benefit",
		"model-id":   "glm-5.3-flash",
		"model-name": "glm-5.3-flash",
	})

	got := headers["Authorization"]
	want := "SDK-HMAC-SHA256 Access=demo-ak-not-real, " +
		"SignedHeaders=content-type;host;maas_type;model-id;model-name;x-sdk-date, " +
		"Signature=0bb3bce2a2070e90afa7e80b3d0f6b11622ba9eebeeb4d2b447693ae68e4ce59"
	if got != want {
		t.Errorf("Authorization 不匹配\n got: %s\nwant: %s", got, want)
	}
}

// TestSignGenericGetMatchesPythonReference 校验通用签名路径（用于 AgentCenter 列表/详情）。
func TestSignGenericGetMatchesPythonReference(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)

	cases := []struct {
		url       string
		signature string
	}{
		{
			url:       "https://snap-access.cn-north-4.myhuaweicloud.com/v1/agent-center/agents/useragents?offset=0&limit=100",
			signature: "0f4c3bb27b3f2ec194ff7ee48c976f1ba1ae1107d0e7399511f77c73244fa58c",
		},
		{
			url:       "https://snap-access.cn-north-4.myhuaweicloud.com/v1/agent-center/agents/detail?agent_id=demo-agent-1",
			signature: "3f31aae7a53b6c92ead002f3f89da2661d0f51aff61df9a1a8b00ab7a16fd5b8",
		},
	}
	for _, tc := range cases {
		headers := creds.SignAt(now, "GET", tc.url, "", nil, nil)
		want := "SDK-HMAC-SHA256 Access=demo-ak-not-real, " +
			"SignedHeaders=content-type;host;x-sdk-date, Signature=" + tc.signature
		if headers["Authorization"] != want {
			t.Errorf("URL %s 签名不匹配\n got: %s\nwant: %s",
				tc.url, headers["Authorization"], want)
		}
	}
}

func splitLines(value string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] == '\n' {
			lines = append(lines, value[start:i])
			start = i + 1
		}
	}
	lines = append(lines, value[start:])
	return lines
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestSignForHostOverridesHost 验证签名 host 可被显式覆盖。
// 场景：通过中转访问上游，签名里的 host 必须是上游真实域名而非中转域名。
func TestSignForHostOverridesHost(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	body := []byte(`{"model":"glm-5.3-flash"}`)
	const upstreamHost = "snap-access.cn-north-4.myhuaweicloud.com"
	const path = "/codearts/api/v2/chat/completions"

	// 场景 A：连接中转域名，但签名 host 写上游真实域名。
	viaRelay := creds.SignAt(now, "POST", "https://relay.example.com"+path, upstreamHost, body, nil)

	if viaRelay["host"] != upstreamHost {
		t.Errorf("签名 host = %q, want %q", viaRelay["host"], upstreamHost)
	}
	if !contains(viaRelay["Authorization"], "host") {
		t.Errorf("SignedHeaders 应包含 host: %q", viaRelay["Authorization"])
	}

	// 场景 B：直接连接上游域名（host 取自 URL）。
	direct := creds.SignAt(now, "POST", "https://"+upstreamHost+path, "", body, nil)

	// 两者的路径与 host 完全相同，签名必须逐字节一致——
	// 这正是修复 401 的依据：中转场景下签名结果等同于直连。
	if viaRelay["Authorization"] != direct["Authorization"] {
		t.Errorf("中转签名应与直连一致\n viaRelay: %s\n   direct: %s",
			viaRelay["Authorization"], direct["Authorization"])
	}
}

// TestSignForHostChangesSignature 验证 host 不同会导致签名不同。
// 这正是 401 的根因：host 写错则验签失败。
func TestSignForHostChangesSignature(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	body := []byte(`{"model":"glm-5.3-flash"}`)
	url := "https://relay.example.com/api/v2/chat/completions"

	withRelay := creds.SignAt(now, "POST", url, "", body, nil)
	withUpstream := creds.SignAt(now, "POST", url, "snap-access.cn-north-4.myhuaweicloud.com", body, nil)

	if withRelay["Authorization"] == withUpstream["Authorization"] {
		t.Fatal("host 不同时签名必须不同")
	}
	if withRelay["host"] != "relay.example.com" {
		t.Errorf("空 signedHost 应回退到 URL host, 得到 %q", withRelay["host"])
	}
}

// TestSignForHostEmptyFallsBackToURLHost 验证不传 host 时保持原有默认行为。
func TestSignForHostEmptyFallsBackToURLHost(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	url := "https://snap-access.cn-north-4.myhuaweicloud.com/api/v2/chat/completions"
	body := []byte(`{}`)

	legacy := creds.SignAt(now, "POST", url, "", body, nil)
	viaSignForHost := creds.SignAt(now, "POST", url, "", body, nil)

	if legacy["Authorization"] != viaSignForHost["Authorization"] {
		t.Error("空 host 时签名应与默认行为完全一致")
	}
	if legacy["host"] != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("host = %q", legacy["host"])
	}
	// 与旧版 python 参考实现的期望签名对比，确认默认路径未被改坏。
	want := "SDK-HMAC-SHA256 Access=demo-ak-not-real, " +
		"SignedHeaders=content-type;host;x-sdk-date, " +
		"Signature=e44ce0168098b53e8583519982096a3bb92ccbe8f919ad283db12f76d8a7d04e"
	if legacy["Authorization"] != want {
		t.Errorf("默认签名行为被改动\n got: %s\nwant: %s", legacy["Authorization"], want)
	}
}

// TestSignForHostRespectsExtraHeadersHost 验证 extraHeaders 里的 host 优先级高于 URL。
func TestSignForHostRespectsExtraHeadersHost(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := New(testAK, testSK)
	url := "https://relay.example.com/api/v2/chat/completions"

	headers := creds.SignAt(now, "POST", url, "", []byte(`{}`),
		map[string]string{"host": "explicit.example.com"})

	if headers["host"] != "explicit.example.com" {
		t.Errorf("extraHeaders 中的 host 应生效, 得到 %q", headers["host"])
	}
}
