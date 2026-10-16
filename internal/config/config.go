// Package config 负责加载运行配置（AK/SK、监听地址、缓存路径）。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 上游端点默认值。可通过 CODEARTS_BASE_URL / CODEARTS_OPENGW_BASE_URL 覆盖
// （用于私有化部署、测试替身或上游域名变更，无需重新编译）。
const (
	DefaultCodeArtsBaseURL = "https://snap-access.cn-north-4.myhuaweicloud.com"
	DefaultOpenGWBaseURL   = "https://opengw.developer.huaweicloud.com"
)

// 各接口相对路径。
const (
	chatPath          = "/api/v2/chat/completions"
	agentListPath     = "/v1/agent-center/agents/useragents?offset=0&limit=100"
	agentDetailPath   = "/v1/agent-center/agents/detail"
	benefitClaimPath  = "/api/v1/benefit/claim"
	balancePath       = "/api/v1/user/tokens/balance"
	gatewayConfigPath = "/api/v1/gateway/config"
)

// Endpoints 汇总上游各接口的完整 URL。
//
// 单独抽出来是为了让基址可配置：调用方（catalog / benefit / server）只依赖
// 这个结构，不直接引用常量，这样改域名或指向测试替身时只需换一个实例。
type Endpoints struct {
	// CodeArtsBase 是 CodeArts 模型与 AgentCenter 的基址。
	CodeArtsBase string
	// OpenGWBase 是免费额度网关（opengw）的基址。
	OpenGWBase string

	// CodeArtsSignedHost / OpenGWSignedHost 是参与签名时使用的 host。
	//
	// 留空表示取对应基址自身的 host（默认行为）。只有通过反向代理或中转访问
	// 上游时才需要设置：上游网关按它认定的域名验签，而中转域名与上游真实域名
	// 不同，会导致验签失败（表现为 401 / APIG.0301）。
	//
	// 两个基址的 host 不同，因此分别配置。
	CodeArtsSignedHost string
	OpenGWSignedHost   string
}

// DefaultEndpoints 返回内置的官方端点。
func DefaultEndpoints() Endpoints {
	return Endpoints{
		CodeArtsBase: DefaultCodeArtsBaseURL,
		OpenGWBase:   DefaultOpenGWBaseURL,
	}
}

// Chat 返回聊天补全接口地址。
func (e Endpoints) Chat() string { return e.CodeArtsBase + chatPath }

// AgentList 返回 AgentCenter 智能体列表地址。
func (e Endpoints) AgentList() string { return e.CodeArtsBase + agentListPath }

// AgentDetail 返回指定智能体的详情地址（agent_id 会被安全转义）。
func (e Endpoints) AgentDetail(agentID string) string {
	return e.CodeArtsBase + agentDetailPath + "?agent_id=" + url.QueryEscape(agentID)
}

// BenefitClaim 返回签到接口地址。
func (e Endpoints) BenefitClaim() string { return e.OpenGWBase + benefitClaimPath }

// Balance 返回额度余额接口地址。
func (e Endpoints) Balance() string { return e.OpenGWBase + balancePath }

// GatewayConfig 返回免费模型列表接口地址。
func (e Endpoints) GatewayConfig() string { return e.OpenGWBase + gatewayConfigPath }

// CodeArtsHost 返回 CodeArts 通道参与签名使用的 host。
//
// 优先取显式配置的 CodeArtsSignedHost（代理+host 覆盖场景）；未配置时取
// CodeArtsBase 自身的 host（替换 BaseUrl 场景，签名自动跟随新域名）。
func (e Endpoints) CodeArtsHost() string {
	if host := strings.TrimSpace(e.CodeArtsSignedHost); host != "" {
		return host
	}
	return hostOf(e.CodeArtsBase)
}

// OpenGWHost 返回免费额度网关参与签名使用的 host，取值规则同 CodeArtsHost。
func (e Endpoints) OpenGWHost() string {
	if host := strings.TrimSpace(e.OpenGWSignedHost); host != "" {
		return host
	}
	return hostOf(e.OpenGWBase)
}

// hostOf 提取 URL 的 host 部分，解析失败时返回空串。
func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// Config 是代理运行所需的全部配置。
type Config struct {
	AK             string
	SK             string
	ListenHost     string
	ListenPort     int
	ModelCachePath string
	// BenefitStatePath 保存自动签到状态（上次成功日期等）。
	BenefitStatePath string
	// AutoClaim 为 true 时，代理每天首次收到请求会顺带签到一次。
	AutoClaim bool
	// Endpoints 是上游接口地址集合，可由环境变量覆盖。
	Endpoints Endpoints
	// ProxyKeys 是允许访问本代理的下游密钥集合。为空表示不鉴权（默认）。
	//
	// 本代理会消耗华为云账号额度，若监听地址暴露到网络，任何知道地址的人都能
	// 白用。设置密钥后，客户端必须在 Authorization 头携带 Bearer <key>。
	ProxyKeys []string
}

// Load 读取 .env 与环境变量并校验必填项。
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		AK:         strings.TrimSpace(os.Getenv("CODEARTS_AK")),
		SK:         strings.TrimSpace(os.Getenv("CODEARTS_SK")),
		ListenHost: firstNonEmpty(os.Getenv("CODEARTS_PROXY_HOST"), "127.0.0.1"),
		ListenPort: 8787,
	}
	if cfg.AK == "" || cfg.SK == "" {
		return nil, errors.New("缺少 CODEARTS_AK / CODEARTS_SK，请在 .env 中配置后重试")
	}

	if raw := strings.TrimSpace(os.Getenv("CODEARTS_PROXY_PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("CODEARTS_PROXY_PORT 非法: %q", raw)
		}
		cfg.ListenPort = port
	}

	cachePath := firstNonEmpty(os.Getenv("CODEARTS_MODEL_CACHE"), "models-cache.json")
	if abs, err := filepath.Abs(cachePath); err == nil {
		cachePath = abs
	}
	cfg.ModelCachePath = cachePath

	statePath := firstNonEmpty(os.Getenv("CODEARTS_BENEFIT_STATE"), "benefit-state.json")
	if abs, err := filepath.Abs(statePath); err == nil {
		statePath = abs
	}
	cfg.BenefitStatePath = statePath

	// 自动签到默认开启（关闭需显式设置 CODEARTS_AUTO_CLAIM=0/false/off/no）。
	cfg.AutoClaim = parseBoolDefault(os.Getenv("CODEARTS_AUTO_CLAIM"), true)

	endpoints, err := loadEndpoints()
	if err != nil {
		return nil, err
	}
	cfg.Endpoints = endpoints

	cfg.ProxyKeys = parseProxyKeys(os.Getenv("CODEARTS_API_KEYS"))

	return cfg, nil
}

// parseProxyKeys 解析下游密钥列表，支持逗号或换行分隔。
//
// 空值返回 nil，表示不鉴权（保持对旧配置的兼容）。
func parseProxyKeys(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	})
	seen := make(map[string]struct{}, len(fields))
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		key := strings.TrimSpace(field)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// loadEndpoints 读取端点基址与签名 host，未配置时使用官方默认值。
//
// 允许覆盖的动机：私有化部署、指向本地测试替身、上游域名变更时无需重新编译。
func loadEndpoints() (Endpoints, error) {
	endpoints := DefaultEndpoints()

	if raw := strings.TrimSpace(os.Getenv("CODEARTS_BASE_URL")); raw != "" {
		normalized, err := normalizeBaseURL(raw)
		if err != nil {
			return Endpoints{}, fmt.Errorf("CODEARTS_BASE_URL 非法: %w", err)
		}
		endpoints.CodeArtsBase = normalized
	}
	if raw := strings.TrimSpace(os.Getenv("CODEARTS_OPENGW_BASE_URL")); raw != "" {
		normalized, err := normalizeBaseURL(raw)
		if err != nil {
			return Endpoints{}, fmt.Errorf("CODEARTS_OPENGW_BASE_URL 非法: %w", err)
		}
		endpoints.OpenGWBase = normalized
	}

	// 签名 host 覆盖：留空即取基址自身 host。
	if raw := strings.TrimSpace(os.Getenv("CODEARTS_BASE_HOST")); raw != "" {
		normalized, err := normalizeSignedHost(raw)
		if err != nil {
			return Endpoints{}, fmt.Errorf("CODEARTS_BASE_HOST 非法: %w", err)
		}
		endpoints.CodeArtsSignedHost = normalized
	}
	if raw := strings.TrimSpace(os.Getenv("CODEARTS_OPENGW_BASE_HOST")); raw != "" {
		normalized, err := normalizeSignedHost(raw)
		if err != nil {
			return Endpoints{}, fmt.Errorf("CODEARTS_OPENGW_BASE_HOST 非法: %w", err)
		}
		endpoints.OpenGWSignedHost = normalized
	}

	return endpoints, nil
}

// normalizeSignedHost 校验签名 host。
//
// 只接受主机名（可带端口），不接受协议前缀或路径——写错会让签名静默失效，
// 所以这里宁可报错也不做"宽容解析"。
func normalizeSignedHost(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("不能为空")
	}
	if strings.Contains(value, "://") {
		return "", errors.New("只写主机名，不要带 http:// 或 https:// 前缀")
	}
	if strings.ContainsAny(value, "/?#") {
		return "", errors.New("只写主机名，不要带路径、查询串或锚点")
	}
	// 借 URL 解析校验主机名合法性（如端口越界、字符非法）。
	parsed, err := url.Parse("https://" + value)
	if err != nil {
		return "", err
	}
	if parsed.Host != value {
		return "", fmt.Errorf("主机名格式非法: %q", value)
	}
	return value, nil
}

// normalizeBaseURL 校验并规范化基址：必须是带主机名的 http/https 地址，
// 且末尾不带斜杠（端点常量都以 "/" 开头，避免拼出双斜杠）。
func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("协议必须是 http 或 https，得到 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("缺少主机名")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("基址不应包含查询串或锚点")
	}
	normalized := strings.TrimSuffix(parsed.String(), "/")
	// 去掉路径末尾的斜杠，保证与端点常量拼接后不出现 "//"。
	return strings.TrimRight(normalized, "/"), nil
}

// parseBoolDefault 解析布尔环境变量，空值时返回默认值。
// 接受 1/true/yes/on 与 0/false/no/off（大小写不敏感）。
func parseBoolDefault(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// Addr 返回 http.Server 可用的监听地址。
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.ListenHost, c.ListenPort)
}

// BaseURL 返回对外暴露的 OpenAI 兼容 Base URL。
func (c *Config) BaseURL() string {
	return fmt.Sprintf("http://%s:%d/v1", c.ListenHost, c.ListenPort)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// loadDotEnv 从当前目录或可执行文件所在目录读取 .env。
// 已存在的环境变量优先，不会被文件覆盖。
func loadDotEnv() {
	for _, dir := range candidateDirs() {
		path := filepath.Join(dir, ".env")
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		applyDotEnv(string(content))
		return
	}
}

func candidateDirs() []string {
	dirs := []string{"."}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	return dirs
}

func applyDotEnv(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}
