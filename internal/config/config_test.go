package config

import (
	"os"
	"path/filepath"
	"testing"
)

// clearEnv 清理可能影响测试的环境变量。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CODEARTS_AK", "CODEARTS_SK", "CODEARTS_PROXY_HOST",
		"CODEARTS_PROXY_PORT", "CODEARTS_MODEL_CACHE",
	} {
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

func TestLoadRequiresCredentials(t *testing.T) {
	clearEnv(t)
	// 切换到无 .env 的临时目录，避免读到真实配置。
	t.Chdir(t.TempDir())

	if _, err := Load(); err == nil {
		t.Fatal("缺少 AK/SK 时应返回错误")
	}
}

func TestLoadReadsEnvFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	content := "# 注释\nexport CODEARTS_AK=demo-ak\nCODEARTS_SK='demo-sk'\nCODEARTS_PROXY_PORT=9999\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AK != "demo-ak" {
		t.Errorf("AK = %q", cfg.AK)
	}
	if cfg.SK != "demo-sk" {
		t.Errorf("SK = %q（引号应被剥离）", cfg.SK)
	}
	if cfg.ListenPort != 9999 {
		t.Errorf("端口 = %d", cfg.ListenPort)
	}
	if cfg.ListenHost != "127.0.0.1" {
		t.Errorf("默认 host = %q", cfg.ListenHost)
	}
	if cfg.Addr() != "127.0.0.1:9999" {
		t.Errorf("Addr = %q", cfg.Addr())
	}
	if cfg.BaseURL() != "http://127.0.0.1:9999/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL())
	}
}

func TestLoadEnvVarsOverrideFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("CODEARTS_AK=from-file\nCODEARTS_SK=sk-file\n"), 0o644)
	t.Chdir(dir)

	// 已存在的环境变量优先，与 python-dotenv 默认行为一致。
	t.Setenv("CODEARTS_AK", "from-env")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AK != "from-env" {
		t.Errorf("AK = %q, 环境变量应优先于 .env", cfg.AK)
	}
	if cfg.SK != "sk-file" {
		t.Errorf("SK = %q", cfg.SK)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("CODEARTS_AK", "a")
	t.Setenv("CODEARTS_SK", "b")
	t.Setenv("CODEARTS_PROXY_PORT", "not-a-port")

	if _, err := Load(); err == nil {
		t.Fatal("非法端口应返回错误")
	}
}

func TestLoadModelCachePathIsAbsolute(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("CODEARTS_AK", "a")
	t.Setenv("CODEARTS_SK", "b")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.ModelCachePath) {
		t.Errorf("缓存路径应为绝对路径: %q", cfg.ModelCachePath)
	}
	if filepath.Base(cfg.ModelCachePath) != "models-cache.json" {
		t.Errorf("默认缓存文件名 = %q", filepath.Base(cfg.ModelCachePath))
	}
}

func TestApplyDotEnvSkipsCommentsAndBlanks(t *testing.T) {
	clearEnv(t)
	applyDotEnv("\n# comment\n   \nNO_EQUALS_LINE\nCODEARTS_TEST_KEY=value\n")
	defer os.Unsetenv("CODEARTS_TEST_KEY")

	if got := os.Getenv("CODEARTS_TEST_KEY"); got != "value" {
		t.Errorf("CODEARTS_TEST_KEY = %q", got)
	}
}
