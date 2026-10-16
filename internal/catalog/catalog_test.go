package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"codearts2api/internal/config"
	"codearts2api/internal/signer"
	"codearts2api/internal/upstream"
)

func newTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	creds := signer.New("demo-ak", "demo-sk")
	return New(
		upstream.New(creds, config.DefaultEndpoints().CodeArtsHost()),
		upstream.New(creds, config.DefaultEndpoints().OpenGWHost()),
		filepath.Join(t.TempDir(), "models-cache.json"),
		config.DefaultEndpoints(),
	)
}

func TestNewFallsBackWhenNoCache(t *testing.T) {
	cat := newTestCatalog(t)

	ids := cat.IDs()
	if len(ids) != len(FallbackAgentModels)+len(FallbackBenefitModels) {
		t.Fatalf("兜底模型数量 = %d, ids = %v", len(ids), ids)
	}
	// 兜底列表中 agent 通道模型应被正确识别。
	if !cat.IsAgentChannel("openpangu-2.0-pro") {
		t.Error("openpangu-2.0-pro 应走 agent 通道")
	}
	// 不在已知列表中的模型按 benefit 处理。
	if cat.IsAgentChannel("glm-5.3-flash") {
		t.Error("glm-5.3-flash 不应被判定为 agent 通道")
	}
	if cat.IsAgentChannel("some-brand-new-model") {
		t.Error("未知模型应默认按 benefit 通道处理")
	}
}

func TestSaveAndLoadCache(t *testing.T) {
	cat := newTestCatalog(t)
	models := []Model{
		{ID: "openpangu-2.0-pro", Channel: ChannelAgent, Name: "盘古", ContextWindow: intPtr(524288)},
		{ID: "glm-5.3-flash", Channel: ChannelBenefit, Name: "GLM", MaxTokens: intPtr(131072)},
	}
	cat.SaveCache(models)

	// 新目录实例应能从缓存恢复。
	reloaded := newTestCatalogAt(t, cat.cachePath)
	ids := reloaded.IDs()
	if len(ids) != 2 || ids[0] != "openpangu-2.0-pro" || ids[1] != "glm-5.3-flash" {
		t.Fatalf("缓存恢复失败: %v", ids)
	}
	if !reloaded.IsAgentChannel("openpangu-2.0-pro") {
		t.Error("缓存中 agent 通道信息丢失")
	}

	model, ok := reloaded.Lookup("openpangu-2.0-pro")
	if !ok || model.ContextWindow == nil || *model.ContextWindow != 524288 {
		t.Errorf("context_window 未恢复: %+v", model)
	}
	if model.Name != "盘古" {
		t.Errorf("name = %q", model.Name)
	}
}

func TestLoadCacheToleratesLegacyChannel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models-cache.json")
	// 旧缓存没有 channel 字段，应默认按 agent 处理。
	legacy := `{"updated_at":1,"models":[{"id":"GLM-5.2","object":"model","owned_by":"codearts","name":"GLM-5.2"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := newTestCatalogAt(t, path)
	if !cat.Contains("GLM-5.2") {
		t.Fatal("旧缓存未装载")
	}
	if !cat.IsAgentChannel("GLM-5.2") {
		t.Error("旧缓存模型应默认视为 agent 通道")
	}
}

func TestLoadCacheRejectsBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models-cache.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := newTestCatalogAt(t, path)
	if !cat.LoadCache() {
		// 预期失败，此时应仍保有兜底列表。
		if len(cat.IDs()) == 0 {
			t.Error("缓存损坏时应保留兜底列表")
		}
	} else {
		t.Error("损坏的缓存不应被装载")
	}
}

func TestSaveCacheWritesAtomically(t *testing.T) {
	cat := newTestCatalog(t)
	cat.SaveCache([]Model{{ID: "m1", Channel: ChannelAgent}})

	raw, err := os.ReadFile(cat.cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var payload CacheFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("缓存 JSON 非法: %v", err)
	}
	if payload.UpdatedAt == 0 || len(payload.Models) != 1 {
		t.Errorf("缓存内容异常: %+v", payload)
	}
	// 临时文件应已被重命名，不留残余。
	if _, err := os.Stat(cat.cachePath + ".tmp"); !os.IsNotExist(err) {
		t.Error("临时文件未被清理")
	}
}

func TestStoreDeduplicatesAndKeepsOrder(t *testing.T) {
	cat := newTestCatalog(t)
	cat.store([]Model{
		{ID: "a", Channel: ChannelAgent},
		{ID: "b", Channel: ChannelBenefit},
		{ID: "a", Channel: ChannelBenefit},
	})

	ids := cat.IDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("去重或顺序错误: %v", ids)
	}
	// 重复项保留首次出现的通道。
	if !cat.IsAgentChannel("a") {
		t.Error("重复 ID 应保留首次出现的 agent 通道")
	}
}

func TestModelsReturnsDetails(t *testing.T) {
	cat := newTestCatalog(t)
	cat.store([]Model{
		{ID: "a", Channel: ChannelAgent, Name: "模型A", SupportsImage: true},
		{ID: "b", Channel: ChannelBenefit},
	})

	models := cat.Models()
	if len(models) != 2 {
		t.Fatalf("models 数量 = %d", len(models))
	}
	if models[0].ID != "a" || models[0].Name != "模型A" || !models[0].SupportsImage {
		t.Errorf("详情错误: %+v", models[0])
	}
	if cat.FirstID() != "a" {
		t.Errorf("FirstID = %q", cat.FirstID())
	}
}

func TestRefreshIfNeededSkipsNetworkWhenCacheFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models-cache.json")
	seed := newTestCatalogAt(t, path)
	seed.SaveCache([]Model{{ID: "cached-model", Channel: ChannelAgent}})

	// 新建实例从新鲜缓存装载；此时不应再发起任何上游请求。
	// 用一个指向不可达地址的客户端来证明这一点：一旦触网就会失败并丢失模型。
	unreachable := upstream.New(signer.New("a", "b"), "unreachable.invalid")
	cat := New(unreachable, unreachable, path, config.DefaultEndpoints())
	cat.RefreshIfNeeded(context.Background())

	if !cat.Contains("cached-model") {
		t.Error("新鲜缓存应保持可用且不触网")
	}
	if cat.FirstID() != "cached-model" {
		t.Errorf("FirstID = %q", cat.FirstID())
	}
}

func intPtr(v int64) *int64 { return &v }

// newTestCatalogAt 构造指向指定缓存路径的目录（不触网，仅用于缓存相关用例）。
func newTestCatalogAt(t *testing.T, path string) *Catalog {
	t.Helper()
	creds := signer.New("a", "b")
	endpoints := config.DefaultEndpoints()
	return New(
		upstream.New(creds, endpoints.CodeArtsHost()),
		upstream.New(creds, endpoints.OpenGWHost()),
		path,
		endpoints,
	)
}
