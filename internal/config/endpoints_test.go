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

// TestCodeArtsHostDefaultsToBaseHost 验证未配置覆盖时，签名 host 取基址自身域名。
func TestCodeArtsHostDefaultsToBaseHost(t *testing.T) {
	endpoints := DefaultEndpoints()

	if got := endpoints.CodeArtsHost(); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("CodeArtsHost() = %q", got)
	}
	if got := endpoints.OpenGWHost(); got != "opengw.developer.huaweicloud.com" {
		t.Errorf("OpenGWHost() = %q", got)
	}
}

// TestHostFollowsReplacedBaseURL 验证替换 BaseUrl 时签名 host 自动跟随新域名。
//
// 这是最常用的场景：直接把 BASE_URL 指向新的上游地址，无需额外配置 host。
func TestHostFollowsReplacedBaseURL(t *testing.T) {
	endpoints := Endpoints{
		CodeArtsBase: "https://new-upstream.example.com/codearts",
		OpenGWBase:   "https://new-gw.example.com/opengw",
	}

	if got := endpoints.CodeArtsHost(); got != "new-upstream.example.com" {
		t.Errorf("CodeArtsHost() = %q, want new-upstream.example.com", got)
	}
	if got := endpoints.OpenGWHost(); got != "new-gw.example.com" {
		t.Errorf("OpenGWHost() = %q, want new-gw.example.com", got)
	}
}

// TestHostOverrideForProxy 验证「代理 + host 覆盖」场景。
//
// 连接走代理域名，但签名必须用上游真实域名。
func TestHostOverrideForProxy(t *testing.T) {
	endpoints := Endpoints{
		CodeArtsBase:       "https://proxy.example.com/codearts",
		OpenGWBase:         "https://proxy.example.com/opengw",
		CodeArtsSignedHost: "snap-access.cn-north-4.myhuaweicloud.com",
		OpenGWSignedHost:   "opengw.developer.huaweicloud.com",
	}

	if got := endpoints.CodeArtsHost(); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("CodeArtsHost() = %q", got)
	}
	if got := endpoints.OpenGWHost(); got != "opengw.developer.huaweicloud.com" {
		t.Errorf("OpenGWHost() = %q", got)
	}
}

// TestHostOverrideIsIndependentPerChannel 验证两个通道的 host 覆盖互不影响。
func TestHostOverrideIsIndependentPerChannel(t *testing.T) {
	endpoints := Endpoints{
		CodeArtsBase:       "https://proxy.example.com/codearts",
		OpenGWBase:         "https://proxy.example.com/opengw",
		CodeArtsSignedHost: "snap-access.cn-north-4.myhuaweicloud.com",
		// OpenGWSignedHost 留空：应回退到基址 host
	}

	if got := endpoints.CodeArtsHost(); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("已覆盖的通道 = %q", got)
	}
	if got := endpoints.OpenGWHost(); got != "proxy.example.com" {
		t.Errorf("未覆盖的通道应取基址 host, 得到 %q", got)
	}
}

// TestLoadEndpointsSignedHostFromEnv 验证签名 host 可由环境变量配置。
func TestLoadEndpointsSignedHostFromEnv(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://proxy.example.com/codearts")
	t.Setenv("CODEARTS_OPENGW_BASE_URL", "https://proxy.example.com/opengw")
	t.Setenv("CODEARTS_BASE_HOST", "snap-access.cn-north-4.myhuaweicloud.com")
	t.Setenv("CODEARTS_OPENGW_BASE_HOST", "opengw.developer.huaweicloud.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Endpoints.CodeArtsHost(); got != "snap-access.cn-north-4.myhuaweicloud.com" {
		t.Errorf("CodeArtsHost() = %q", got)
	}
	if got := cfg.Endpoints.OpenGWHost(); got != "opengw.developer.huaweicloud.com" {
		t.Errorf("OpenGWHost() = %q", got)
	}
}

// TestSignedHostEnvDefaultsToBaseHost 验证不配置 host 时跟随基址。
func TestSignedHostEnvDefaultsToBaseHost(t *testing.T) {
	prepareEnv(t)
	t.Setenv("CODEARTS_BASE_URL", "https://new-upstream.example.com/codearts")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoints.CodeArtsSignedHost != "" {
		t.Errorf("未配置时 CodeArtsSignedHost 应为空, 得到 %q", cfg.Endpoints.CodeArtsSignedHost)
	}
	if got := cfg.Endpoints.CodeArtsHost(); got != "new-upstream.example.com" {
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
	if got := cfg.Endpoints.CodeArtsHost(); got != "127.0.0.1:9999" {
		t.Errorf("CodeArtsHost() = %q", got)
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
