// Package signer 实现华为云 SDK-HMAC-SHA256 请求签名。
//
// 签名规范要点（与 CodeArts IDE 抓包行为一致）：
//   - 参与签名的消息头名称一律转小写，按字典序排列；
//   - CanonicalURI 必须以 "/" 结尾，不以 "/" 结尾的路径会补一个；
//   - 查询串按 (key, value) 排序后重新百分号编码，安全字符集为 A-Za-z0-9-_.~；
//   - CanonicalHeaders 每行形如 "name:value\n"，值首尾空白需剔除；
//   - StringToSign = "SDK-HMAC-SHA256\n" + x-sdk-date + "\n" + sha256(CanonicalRequest)。
package signer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Algorithm 是华为云签名算法标识。
const Algorithm = "SDK-HMAC-SHA256"

// sdkDateFormat 是 x-sdk-date 使用的时间格式（UTC）。
const sdkDateFormat = "20060102T150405Z"

// Credentials 保存签名所需的密钥对。
type Credentials struct {
	AK string
	SK string
}

// New 构造一个签名器。
func New(ak, sk string) *Credentials {
	return &Credentials{AK: ak, SK: sk}
}

// SDKDate 返回当前 UTC 时间对应的 x-sdk-date 值。
func SDKDate(now time.Time) string {
	return now.UTC().Format(sdkDateFormat)
}

// SignForHost 计算签名并返回可写入 HTTP 请求的头部集合。
//
// signedHost 指定参与签名的 host，传空串表示取 rawURL 的 host。
// 需要显式指定 host 的场景：通过反向代理/中转访问上游时，连接地址与上游
// 认定的域名不一致。上游网关按它收到的 Host 头验签，因此签名里的 host 必须
// 写上游真实域名，而不是中转域名——否则验签失败（表现为 401 / APIG.0301）。
func (c *Credentials) SignForHost(method, rawURL, signedHost string, body []byte, extraHeaders map[string]string) map[string]string {
	return c.SignAt(time.Now(), method, rawURL, signedHost, body, extraHeaders)
}

// SignAt 与 SignForHost 相同，但使用调用方指定的时间生成 x-sdk-date，便于测试复现。
//
// signedHost 为空时取 rawURL 的 host（默认行为）；非空时以其为准，
// 并同时作为实际发出的 Host 头，保证"签什么就发什么"。
func (c *Credentials) SignAt(now time.Time, method, rawURL, signedHost string, body []byte, extraHeaders map[string]string) map[string]string {
	headers := c.canonicalHeaders(rawURL, signedHost, extraHeaders)
	sdkDate := SDKDate(now)
	headers["x-sdk-date"] = sdkDate

	parsed := parseURL(rawURL)
	canonicalRequest := CanonicalRequest(method, parsed, headers, body)
	stringToSign := Algorithm + "\n" + sdkDate + "\n" + sha256Hex([]byte(canonicalRequest))
	signature := hmacHex(c.SK, stringToSign)

	headers["Authorization"] = Algorithm + " Access=" + c.AK +
		", SignedHeaders=" + SignedHeaderNames(headers) + ", Signature=" + signature
	return headers
}

// canonicalHeaders 组装参与签名的头集合。
//
// host 的取值优先级：显式 signedHost > extraHeaders 中的 host > URL 自带 host。
func (c *Credentials) canonicalHeaders(rawURL, signedHost string, extraHeaders map[string]string) map[string]string {
	headers := make(map[string]string, len(extraHeaders)+4)
	for name, value := range extraHeaders {
		headers[strings.ToLower(name)] = value
	}
	if _, ok := headers["content-type"]; !ok {
		headers["content-type"] = "application/json"
	}
	headers["host"] = resolveSignedHost(rawURL, signedHost, headers["host"])
	return headers
}

// resolveSignedHost 决定签名使用的 host。
func resolveSignedHost(rawURL, signedHost, fromHeaders string) string {
	if value := strings.TrimSpace(signedHost); value != "" {
		return value
	}
	if value := strings.TrimSpace(fromHeaders); value != "" {
		return value
	}
	return parseURL(rawURL).Host
}

// parseURL 解析 URL，失败时返回一个安全的兜底值。
func parseURL(rawURL string) *url.URL {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return &url.URL{Path: "/"}
	}
	return parsed
}

// CanonicalRequest 构造规范化请求串。
func CanonicalRequest(method string, parsed *url.URL, headers map[string]string, body []byte) string {
	canonicalURI := parsed.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	if !strings.HasSuffix(canonicalURI, "/") {
		canonicalURI += "/"
	}

	names := SignedHeaderNamesList(headers)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headers[name]))
		canonicalHeaders.WriteString("\n")
	}

	return strings.Join([]string{
		strings.ToUpper(method),
		canonicalURI,
		CanonicalQuery(parsed.RawQuery),
		canonicalHeaders.String(),
		strings.Join(names, ";"),
		sha256Hex(body),
	}, "\n")
}

// CanonicalQuery 按 (key, value) 排序并重新编码查询串。
func CanonicalQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	pairs := make([][2]string, 0, 8)
	for _, item := range strings.Split(rawQuery, "&") {
		if item == "" {
			continue
		}
		key, value, found := strings.Cut(item, "=")
		if !found {
			value = ""
		}
		pairs = append(pairs, [2]string{percentDecode(key), percentDecode(value)})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})

	encoded := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		encoded = append(encoded, PercentEncode(pair[0])+"="+PercentEncode(pair[1]))
	}
	return strings.Join(encoded, "&")
}

// SignedHeaderNames 返回分号连接的签名头名称（字典序、小写）。
func SignedHeaderNames(headers map[string]string) string {
	return strings.Join(SignedHeaderNamesList(headers), ";")
}

// SignedHeaderNamesList 返回排序后的签名头名称列表。
func SignedHeaderNamesList(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		if strings.EqualFold(name, "authorization") {
			continue
		}
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	return names
}

// PercentEncode 按 RFC 3986 非保留字符集编码，与 urllib.parse.quote(safe="-_.~") 等价。
func PercentEncode(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[ch>>4])
		b.WriteByte(hexDigits[ch&0x0F])
	}
	return b.String()
}

func percentDecode(value string) string {
	if !strings.Contains(value, "%") && !strings.Contains(value, "+") {
		return value
	}
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacHex(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
