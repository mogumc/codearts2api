package config

import (
	"os"
	"testing"
)

// clearEndpointEnv 清理端点相关环境变量。
func clearEndpointEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CODEARTS_BASE_URL", "CODEARTS_OPENGW_BASE_URL"} {
		old, existed := os.LookupEnv(key)
		_ = os.Unsetenv(key)
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, old)
				return
			}
			_ = os.Unsetenv(key)
		})
	}
}

// prepareEnv 准备一个可成功加载配置的最小环境。
func prepareEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	clearEndpointEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("CODEARTS_AK", "demo-ak")
	t.Setenv("CODEARTS_SK", "demo-sk")
}

func TestDefaultEndpoints(t *testing.T) {
	endpoints := DefaultEndpoints()
	if endpoints.CodeArtsBase != DefaultCodeArtsBaseURL {
		t.Errorf("CodeArtsBase = %q", endpoints.CodeArtsBase)
	}
	if endpoints.OpenGWBase != DefaultOpenGWBaseURL {
		t.Errorf("OpenGWBase = %q", endpoints.OpenGWBase)
	}
}

// TestEndpointPaths 验证各接口 URL 拼接正确（无重复或缺失斜杠）。
func TestEndpointPaths(t *testing.T) {
	endpoints := DefaultEndpoints()

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Chat", endpoints.Chat(), DefaultCodeArtsBaseURL + "/api/v2/chat/completions"},
		{"AgentList", endpoints.AgentList(),
			DefaultCodeArtsBaseURL + "/v1/agent-center/agents/useragents?offset=0&limit=100"},
		{"AgentDetail", endpoints.AgentDetail("abc"),
			DefaultCodeArtsBaseURL + "/v1/agent-center/agents/detail?agent_id=abc"},
		{"BenefitClaim", endpoints.BenefitClaim(), DefaultOpenGWBaseURL + "/api/v1/benefit/claim"},
		{"Balance", endpoints.Balance(), DefaultOpenGWBaseURL + "/api/v1/user/tokens/balance"},
		{"GatewayConfig", endpoints.GatewayConfig(), DefaultOpenGWBaseURL + "/api/v1/gateway/config"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q\n     want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestAgentDetailEscapesID 验证 agent_id 被安全转义，避免注入查询串。
func TestAgentDetailEscapesID(t *testing.T) {
	endpoints := DefaultEndpoints()
	got := endpoints.AgentDetail("a&b=c d")
	want := DefaultCodeArtsBaseURL + "/v1/agent-center/agents/detail?agent_id=a%26b%3Dc+d"
	if got != want {
		t.Errorf("AgentDetail = %q\n        want %q", got, want)
	}
}

// TestEndpointsOverriddenByEnv 是本次功能的核心用例：基址可被环境变量覆盖。
func TestEndpointsOverriddenByEnv(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://codearts.example.com")
	t.Setenv("CODEARTS_OPENGW_BASE_URL", "https://opengw.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsBase != "https://codearts.example.com" {
		t.Errorf("CodeArtsBase = %q", cfg.Endpoints.CodeArtsBase)
	}
	if cfg.Endpoints.OpenGWBase != "https://opengw.example.com" {
		t.Errorf("OpenGWBase = %q", cfg.Endpoints.OpenGWBase)
	}
	// 覆盖后各接口应指向新域名。
	if got := cfg.Endpoints.Chat(); got != "https://codearts.example.com/api/v2/chat/completions" {
		t.Errorf("Chat = %q", got)
	}
	if got := cfg.Endpoints.BenefitClaim(); got != "https://opengw.example.com/api/v1/benefit/claim" {
		t.Errorf("BenefitClaim = %q", got)
	}
}

// TestEndpointsPartialOverride 验证只覆盖其中一个基址时另一个保持默认。
func TestEndpointsPartialOverride(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "http://127.0.0.1:9999")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsBase != "http://127.0.0.1:9999" {
		t.Errorf("CodeArtsBase = %q", cfg.Endpoints.CodeArtsBase)
	}
	if cfg.Endpoints.OpenGWBase != DefaultOpenGWBaseURL {
		t.Errorf("未覆盖的 OpenGWBase 应保持默认, 得到 %q", cfg.Endpoints.OpenGWBase)
	}
}

// TestEndpointsTrailingSlashTrimmed 验证末尾斜杠被去掉，避免拼出双斜杠。
func TestEndpointsTrailingSlashTrimmed(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://codearts.example.com/")
	t.Setenv("CODEARTS_OPENGW_BASE_URL", "https://opengw.example.com///")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsBase != "https://codearts.example.com" {
		t.Errorf("CodeArtsBase = %q", cfg.Endpoints.CodeArtsBase)
	}
	if cfg.Endpoints.OpenGWBase != "https://opengw.example.com" {
		t.Errorf("OpenGWBase = %q", cfg.Endpoints.OpenGWBase)
	}
	if got := cfg.Endpoints.Chat(); got != "https://codearts.example.com/api/v2/chat/completions" {
		t.Errorf("Chat 出现双斜杠: %q", got)
	}
}

// TestEndpointsPreservesBasePath 验证带路径的基址（如网关前缀）被保留。
func TestEndpointsPreservesBasePath(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://gateway.example.com/codearts")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Endpoints.Chat(); got != "https://gateway.example.com/codearts/api/v2/chat/completions" {
		t.Errorf("Chat = %q", got)
	}
}

// TestEndpointsRejectsInvalidURL 验证非法基址被拒绝，避免运行时才暴露问题。
func TestEndpointsRejectsInvalidURL(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"缺少协议", "codearts.example.com"},
		{"非 http 协议", "ftp://codearts.example.com"},
		{"缺少主机名", "https://"},
		{"带查询串", "https://codearts.example.com?x=1"},
		{"带锚点", "https://codearts.example.com#frag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepareEnv(t)
			t.Setenv("CODEARTS_BASE_URL", tc.value)

			if _, err := Load(); err == nil {
				t.Errorf("非法基址 %q 应返回错误", tc.value)
			}
		})
	}
}

// TestEndpointsRejectsInvalidOpenGW 验证 opengw 基址同样被校验。
func TestEndpointsRejectsInvalidOpenGW(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_OPENGW_BASE_URL", "not-a-url")

	if _, err := Load(); err == nil {
		t.Error("非法 opengw 基址应返回错误")
	}
}

// TestNormalizeBaseURL 覆盖规范化细节。
func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://a.example.com":      "https://a.example.com",
		"https://a.example.com/":     "https://a.example.com",
		"https://a.example.com///":   "https://a.example.com",
		"http://127.0.0.1:8080":      "http://127.0.0.1:8080",
		"https://a.example.com/api":  "https://a.example.com/api",
		"https://a.example.com/api/": "https://a.example.com/api",
	}
	for input, want := range cases {
		got, err := normalizeBaseURL(input)
		if err != nil {
			t.Errorf("normalizeBaseURL(%q) 返回错误: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSignedHostForDefaultsToBaseHost 验证未配置签名 host 时取基址自身 host。
func TestSignedHostForDefaultsToBaseHost(t *testing.T) {
	endpoints := DefaultEndpoints()

	cases := map[string]string{
		endpoints.Chat():          "snap-access.cn-north-4.myhuaweicloud.com",
		endpoints.AgentList():     "snap-access.cn-north-4.myhuaweicloud.com",
		endpoints.BenefitClaim():  "opengw.developer.huaweicloud.com",
		endpoints.GatewayConfig(): "opengw.developer.huaweicloud.com",
	}
	for rawURL, want := range cases {
		if got := endpoints.SignedHostFor(rawURL); got != want {
			t.Errorf("SignedHostFor(%s) = %q, want %q", rawURL, got, want)
		}
	}
}

// TestSignedHostForOverride 是本次功能的核心：签名 host 可独立配置。
// 场景：连接中转域名，但签名必须用上游真实域名。
func TestSignedHostForOverride(t *testing.T) {
	endpoints := Endpoints{
		CodeArtsBase:       "https://relay.example.com/codearts",
		OpenGWBase:         "https://relay.example.com/opengw",
		CodeArtsSignedHost: "snap-access.cn-north-4.myhuaweicloud.com",
		OpenGWSignedHost:   "opengw.developer.huaweicloud.com",
	}

	// CodeArts 通道的请求应用 CodeArtsSignedHost。
	if got := endpoints.SignedHostFor(endpoints.Chat()); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("Chat 签名 host = %q", got)
	}
	if got := endpoints.SignedHostFor(endpoints.AgentList()); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("AgentList 签名 host = %q", got)
	}
	// opengw 通道应用 OpenGWSignedHost——两个基址 host 不同，必须分别配置。
	if got := endpoints.SignedHostFor(endpoints.BenefitClaim()); got != "opengw.developer.huaweicloud.com" {
		t.Errorf("BenefitClaim 签名 host = %q", got)
	}
	if got := endpoints.SignedHostFor(endpoints.GatewayConfig()); got != "opengw.developer.huaweicloud.com" {
		t.Errorf("GatewayConfig 签名 host = %q", got)
	}
}

// TestSignedHostForPartialOverride 验证只覆盖一个通道时另一个保持默认。
func TestSignedHostForPartialOverride(t *testing.T) {
	endpoints := Endpoints{
		CodeArtsBase:       "https://relay.example.com/codearts",
		OpenGWBase:         "https://relay.example.com/opengw",
		CodeArtsSignedHost: "snap-access.cn-north-4.myhuaweicloud.com",
		// OpenGWSignedHost 留空
	}

	if got := endpoints.SignedHostFor(endpoints.Chat()); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("已覆盖的通道 = %q", got)
	}
	// 未覆盖的通道退回基址 host（即中转域名）。
	if got := endpoints.SignedHostFor(endpoints.BenefitClaim()); got != "relay.example.com" {
		t.Errorf("未覆盖的通道应取基址 host, 得到 %q", got)
	}
}

// TestSignedHostForUnknownURL 验证无法匹配基址时退回 URL 自带 host。
func TestSignedHostForUnknownURL(t *testing.T) {
	endpoints := DefaultEndpoints()
	got := endpoints.SignedHostFor("https://other.example.com/some/path")
	if got != "other.example.com" {
		t.Errorf("未知 URL 应取自身 host, 得到 %q", got)
	}
}

// TestLoadEndpointsSignedHostFromEnv 验证签名 host 可由环境变量配置。
func TestLoadEndpointsSignedHostFromEnv(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://relay.example.com/codearts")
	t.Setenv("CODEARTS_OPENGW_BASE_URL", "https://relay.example.com/opengw")
	t.Setenv("CODEARTS_BASE_HOST", "snap-access.cn-north-4.myhuaweicloud.com")
	t.Setenv("CODEARTS_OPENGW_BASE_HOST", "opengw.developer.huaweicloud.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsSignedHost != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("CodeArtsSignedHost = %q", cfg.Endpoints.CodeArtsSignedHost)
	}
	if cfg.Endpoints.OpenGWSignedHost != "opengw.developer.huaweicloud.com" {
		t.Errorf("OpenGWSignedHost = %q", cfg.Endpoints.OpenGWSignedHost)
	}
	// 连接地址仍是中转，签名 host 已是上游真实域名。
	if got := cfg.Endpoints.SignedHostFor(cfg.Endpoints.Chat()); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("签名 host = %q", got)
	}
}

// TestSignedHostEnvDefaultsToBaseHost 验证不配置时签名 host 跟随基址。
func TestSignedHostEnvDefaultsToBaseHost(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://relay.example.com/codearts")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsSignedHost != "" {
		t.Errorf("未配置时 CodeArtsSignedHost 应为空, 得到 %q", cfg.Endpoints.CodeArtsSignedHost)
	}
	if got := cfg.Endpoints.SignedHostFor(cfg.Endpoints.Chat()); got != "relay.example.com" {
		t.Errorf("应回退到基址 host, 得到 %q", got)
	}
}

// TestSignedHostEnvRejectsInvalid 验证非法签名 host 被拒绝。
func TestSignedHostEnvRejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"带协议前缀", "https://snap-access.cn-north-4.myhuaweicloud.com"},
		{"带路径", "snap-access.cn-north-4.myhuaweicloud.com/api"},
		{"带查询串", "snap-access.cn-north-4.myhuaweicloud.com?x=1"},
		{"带锚点", "snap-access.cn-north-4.myhuaweicloud.com#f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepareEnv(t)
			t.Setenv("CODEARTS_BASE_HOST", tc.value)
			if _, err := Load(); err == nil {
				t.Errorf("非法 host %q 应返回错误", tc.value)
			}
		})
	}
}

// TestSignedHostAllowsPort 验证带端口的 host 可用（本地测试替身场景）。
func TestSignedHostAllowsPort(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_HOST", "127.0.0.1:9999")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsSignedHost != "127.0.0.1:9999" {
		t.Errorf("CodeArtsSignedHost = %q", cfg.Endpoints.CodeArtsSignedHost)
	}
}

// TestParseProxyKeys 验证下游密钥解析。
func TestParseProxyKeys(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"空值不鉴权", "", nil},
		{"仅空白", "   ", nil},
		{"单个", "sk-1", []string{"sk-1"}},
		{"逗号分隔", "sk-1,sk-2", []string{"sk-1", "sk-2"}},
		{"带空格", " sk-1 , sk-2 ", []string{"sk-1", "sk-2"}},
		{"换行分隔", "sk-1\nsk-2", []string{"sk-1", "sk-2"}},
		{"分号分隔", "sk-1;sk-2", []string{"sk-1", "sk-2"}},
		{"去重", "sk-1,sk-1,sk-2", []string{"sk-1", "sk-2"}},
		{"忽略空段", "sk-1,,sk-2,", []string{"sk-1", "sk-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProxyKeys(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("parseProxyKeys(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 项 = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestProxyKeysFromEnv 验证密钥可从环境变量加载。
func TestProxyKeysFromEnv(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_API_KEYS", "sk-a,sk-b")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProxyKeys) != 2 {
		t.Fatalf("ProxyKeys = %v", cfg.ProxyKeys)
	}
}
