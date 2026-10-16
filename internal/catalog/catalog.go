// Package catalog 维护当前账号可用模型列表。
//
// 模型来自两条互斥通道：
//   - agent：CodeArts AgentCenter 老套餐模型，走基础签名；
//   - benefit：opengw 免费额度模型，签名时必须额外携带 maas_type / model-id /
//     model-name 三个头，否则上游报 "model is not registered"。
//
// 不在已知列表中的模型一律按 benefit 处理，这样上游新增免费模型无需改代码。
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"codearts2api/internal/config"
	"codearts2api/internal/upstream"
)

// 通道标识。
const (
	ChannelAgent   = "agent"
	ChannelBenefit = "benefit"
)

// cacheTTL 是模型列表的短时缓存周期，避免客户端频繁轮询时重复请求上游。
const cacheTTL = 300 * time.Second

// FallbackAgentModels 是 AgentCenter 同步失败时的兜底列表。
var FallbackAgentModels = []string{"openpangu-2.0-pro", "openpangu-2.0-flash", "GLM-5.2"}

// FallbackBenefitModels 是 opengw 同步失败时的兜底列表。
var FallbackBenefitModels = []string{"glm-5.3-flash", "deepseek-v4-pro-0813", "deepseek-v4-flash-0731"}

// Model 描述一个模型的能力元数据。
type Model struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	OwnedBy       string `json:"owned_by"`
	Channel       string `json:"channel"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContextWindow *int64 `json:"context_window,omitempty"`
	MaxTokens     *int64 `json:"max_tokens,omitempty"`
	SupportsImage bool   `json:"supports_images"`
	EnableQueue   bool   `json:"enable_queue,omitempty"`
}

// CacheFile 是 models-cache.json 的结构。
type CacheFile struct {
	UpdatedAt int64   `json:"updated_at"`
	Models    []Model `json:"models"`
}

// Catalog 是并发安全的模型目录。
type Catalog struct {
	client    *upstream.Client
	cachePath string
	endpoints config.Endpoints

	refreshMu sync.Mutex

	mu       sync.RWMutex
	order    []string
	details  map[string]Model
	agentIDs map[string]struct{}
}

// New 构造模型目录：优先装载本地缓存，缓存不可用时退回内置兜底列表。
// endpoints 决定从哪个上游同步模型列表。
func New(client *upstream.Client, cachePath string, endpoints config.Endpoints) *Catalog {
	c := &Catalog{
		client:    client,
		cachePath: cachePath,
		endpoints: endpoints,
		details:   map[string]Model{},
		agentIDs:  map[string]struct{}{},
	}
	if !c.LoadCache() {
		c.applyFallback()
	}
	return c
}

// IDs 返回当前可用模型 ID 列表（保持发现顺序）。
func (c *Catalog) IDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

// Models 返回当前可用模型详情列表。
func (c *Catalog) Models() []Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Model, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.details[id])
	}
	return out
}

// Lookup 按 ID 查找模型详情。
func (c *Catalog) Lookup(id string) (Model, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	model, ok := c.details[id]
	return model, ok
}

// Contains 判断模型是否在当前可用列表中。
func (c *Catalog) Contains(id string) bool {
	_, ok := c.Lookup(id)
	return ok
}

// IsAgentChannel 判断模型是否走 AgentCenter（老套餐）通道。
func (c *Catalog) IsAgentChannel(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.agentIDs[id]
	return ok
}

// FirstID 返回首个可用模型 ID，作为未指定 model 时的默认值。
func (c *Catalog) FirstID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.order) == 0 {
		return ""
	}
	return c.order[0]
}

// RefreshIfNeeded 在缓存过期时同步模型列表；同步失败则回退到本地缓存。
// 并发调用时只允许一个 goroutine 真正发起上游请求，避免客户端轮询造成请求风暴。
func (c *Catalog) RefreshIfNeeded(ctx context.Context) {
	c.mu.RLock()
	empty := len(c.details) == 0
	c.mu.RUnlock()
	if empty {
		c.LoadCache()
	}

	if c.cacheAge() <= cacheTTL {
		return
	}

	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// 拿到锁后重新判断，可能已被其他 goroutine 刷新过。
	if c.cacheAge() <= cacheTTL {
		return
	}
	if !c.Refresh(ctx) {
		c.LoadCache()
	}
}

func (c *Catalog) cacheAge() time.Duration {
	info, err := os.Stat(c.cachePath)
	if err != nil {
		return time.Duration(1<<62 - 1)
	}
	return time.Since(info.ModTime())
}

// Refresh 从上游同步模型列表，成功返回 true。
func (c *Catalog) Refresh(ctx context.Context) bool {
	discovered, ok := c.fetchAgentModels(ctx)
	if !ok {
		return false
	}

	benefitModels := c.fetchBenefitModels(ctx)
	if len(benefitModels) == 0 {
		benefitModels = fallbackBenefitModels()
	}

	known := make(map[string]struct{}, len(discovered))
	for _, m := range discovered {
		known[m.ID] = struct{}{}
	}
	for _, m := range benefitModels {
		if _, dup := known[m.ID]; dup {
			continue
		}
		known[m.ID] = struct{}{}
		discovered = append(discovered, m)
	}

	c.store(discovered)
	c.SaveCache(discovered)
	return true
}

// LoadCache 从本地缓存装载模型列表。
func (c *Catalog) LoadCache() bool {
	raw, err := os.ReadFile(c.cachePath)
	if err != nil {
		return false
	}
	var cached CacheFile
	if err := json.Unmarshal(raw, &cached); err != nil {
		return false
	}
	if len(cached.Models) == 0 {
		return false
	}
	models := make([]Model, 0, len(cached.Models))
	for _, m := range cached.Models {
		if m.ID == "" {
			continue
		}
		if m.Channel == "" {
			// 旧缓存无 channel 字段时默认视为 agent 通道。
			m.Channel = ChannelAgent
		}
		if m.Object == "" {
			m.Object = "model"
		}
		if m.OwnedBy == "" {
			m.OwnedBy = "codearts"
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		return false
	}
	c.store(models)
	return true
}

// SaveCache 原子写入模型缓存。
func (c *Catalog) SaveCache(models []Model) {
	if err := os.MkdirAll(filepath.Dir(c.cachePath), 0o755); err != nil {
		return
	}
	payload, err := json.MarshalIndent(CacheFile{
		UpdatedAt: time.Now().Unix(),
		Models:    models,
	}, "", "  ")
	if err != nil {
		return
	}
	tempPath := c.cachePath + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return
	}
	_ = os.Rename(tempPath, c.cachePath)
}

// applyFallback 装载内置兜底模型。
func (c *Catalog) applyFallback() {
	models := make([]Model, 0, len(FallbackAgentModels)+len(FallbackBenefitModels))
	for _, id := range FallbackAgentModels {
		models = append(models, Model{
			ID: id, Object: "model", OwnedBy: "codearts",
			Channel: ChannelAgent, Name: id,
		})
	}
	models = append(models, fallbackBenefitModels()...)
	c.store(models)
}

func fallbackBenefitModels() []Model {
	models := make([]Model, 0, len(FallbackBenefitModels))
	for _, id := range FallbackBenefitModels {
		models = append(models, Model{
			ID: id, Object: "model", OwnedBy: "codearts",
			Channel: ChannelBenefit, Name: id,
			Description: "免费额度模型（签到领取）",
		})
	}
	return models
}

// store 用新列表替换当前目录内容。
func (c *Catalog) store(models []Model) {
	order := make([]string, 0, len(models))
	details := make(map[string]Model, len(models))
	agentIDs := map[string]struct{}{}
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		if _, dup := details[m.ID]; dup {
			continue
		}
		order = append(order, m.ID)
		details[m.ID] = m
		if m.Channel == ChannelAgent {
			agentIDs[m.ID] = struct{}{}
		}
	}
	c.mu.Lock()
	c.order = order
	c.details = details
	c.agentIDs = agentIDs
	c.mu.Unlock()
}

// fetchAgentModels 从 AgentCenter 拉取老套餐模型列表。
func (c *Catalog) fetchAgentModels(ctx context.Context) ([]Model, bool) {
	headers := map[string]string{
		"agent-type": "AgentCenter",
		"x-language": "zh-cn",
		"accept":     "application/json",
	}

	resp, err := c.client.Do(ctx, http.MethodGet, c.endpoints.AgentList(), nil, headers)
	if err != nil {
		fmt.Println("自动同步模型失败，将使用缓存或兜底列表:", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("自动同步模型失败，将使用缓存或兜底列表: HTTP %d\n", resp.StatusCode)
		return nil, false
	}

	var list struct {
		Agents []struct {
			AgentID       string `json:"agent_id"`
			IsPrimary     bool   `json:"is_primary_agent"`
			AgentOrder    *int   `json:"agent_order"`
			PrimaryAgent  bool   `json:"is_primary"`
			AgentOrderAlt *int   `json:"order"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list.Agents) == 0 {
		if err != nil {
			fmt.Println("自动同步模型失败，将使用缓存或兜底列表:", err)
		}
		return nil, false
	}

	// 优先主 Agent；主 Agent 无模型时再尝试其他。
	agents := make([]agentRef, 0, len(list.Agents))
	for _, a := range list.Agents {
		if a.AgentID == "" {
			continue
		}
		agents = append(agents, agentRef{
			id:      a.AgentID,
			primary: a.IsPrimary || a.PrimaryAgent,
			order:   pickOrder(a.AgentOrder, a.AgentOrderAlt),
		})
	}
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].primary != agents[j].primary {
			return agents[i].primary
		}
		return agents[i].order < agents[j].order
	})

	var discovered []Model
	for _, agent := range agents {
		detailURL := c.endpoints.AgentDetail(agent.id)
		models, ok := c.fetchAgentDetail(ctx, detailURL, headers)
		if !ok {
			continue
		}
		discovered = append(discovered, models...)
		if len(discovered) > 0 {
			// 主 Agent 已拿到完整模型列表，避免不必要请求。
			break
		}
	}
	if len(discovered) == 0 {
		return nil, false
	}
	return discovered, true
}

type agentRef struct {
	id      string
	primary bool
	order   int
}

func pickOrder(values ...*int) int {
	for _, v := range values {
		if v != nil {
			return *v
		}
	}
	return 999999
}

func (c *Catalog) fetchAgentDetail(ctx context.Context, detailURL string, headers map[string]string) ([]Model, bool) {
	resp, err := c.client.Do(ctx, http.MethodGet, detailURL, nil, headers)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}

	var detail struct {
		GPTs struct {
			Models []struct {
				ModelID     string `json:"model_id"`
				ModelAlias  string `json:"model_alias"`
				ModelName   string `json:"model_name"`
				ModelDesc   string `json:"model_desc"`
				ModelParams struct {
					ContextWindow *int64 `json:"context_window"`
					MaxTokens     *int64 `json:"max_tokens"`
					SupportsImage bool   `json:"supports_images"`
					EnableQueue   bool   `json:"enable_queue"`
				} `json:"model_parameters"`
			} `json:"models"`
		} `json:"gpts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, false
	}

	var models []Model
	for _, m := range detail.GPTs.Models {
		id := firstNonEmpty(m.ModelID, m.ModelAlias)
		if id == "" {
			continue
		}
		models = append(models, Model{
			ID:            id,
			Object:        "model",
			OwnedBy:       "codearts",
			Channel:       ChannelAgent,
			Name:          firstNonEmpty(m.ModelAlias, m.ModelName, id),
			Description:   m.ModelDesc,
			ContextWindow: m.ModelParams.ContextWindow,
			MaxTokens:     m.ModelParams.MaxTokens,
			SupportsImage: m.ModelParams.SupportsImage,
			EnableQueue:   m.ModelParams.EnableQueue,
		})
	}
	return models, len(models) > 0
}

// fetchBenefitModels 从 opengw 网关拉取免费模型列表。
func (c *Catalog) fetchBenefitModels(ctx context.Context) []Model {
	resp, err := c.client.Do(ctx, http.MethodGet, c.endpoints.GatewayConfig(), nil, nil)
	if err != nil {
		fmt.Println("免费模型列表同步失败，将使用兜底列表:", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("免费模型列表同步失败，将使用兜底列表: HTTP %d\n", resp.StatusCode)
		return nil
	}

	var payload struct {
		Result struct {
			Models []struct {
				ModelID       string `json:"model_id"`
				ModelName     string `json:"model_name"`
				ContextWindow *int64 `json:"context_window"`
				MaxTokens     *int64 `json:"max_tokens"`
			} `json:"models"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		fmt.Println("免费模型列表同步失败，将使用兜底列表:", err)
		return nil
	}

	seen := map[string]struct{}{}
	var discovered []Model
	for _, m := range payload.Result.Models {
		if m.ModelID == "" {
			continue
		}
		if _, dup := seen[m.ModelID]; dup {
			continue
		}
		seen[m.ModelID] = struct{}{}
		discovered = append(discovered, Model{
			ID:            m.ModelID,
			Object:        "model",
			OwnedBy:       "codearts",
			Channel:       ChannelBenefit,
			Name:          firstNonEmpty(m.ModelName, m.ModelID),
			Description:   "免费额度模型（签到领取）",
			ContextWindow: m.ContextWindow,
			MaxTokens:     m.MaxTokens,
		})
	}
	return discovered
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
