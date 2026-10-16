// Command codearts2api 把华为云 CodeArts 模型接口包装成 OpenAI 兼容 API。
//
// 用法:
//
//	codearts2api            # 启动代理（默认 127.0.0.1:8787）
//	codearts2api benefit    # 免费额度签到 + 查询余额（详见 benefit 子命令）
//	codearts2api -version   # 打印版本信息
//
// 配置来自 .env 或环境变量：CODEARTS_AK / CODEARTS_SK。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"codearts2api/internal/benefit"
	"codearts2api/internal/catalog"
	"codearts2api/internal/config"
	"codearts2api/internal/server"
	"codearts2api/internal/signer"
	"codearts2api/internal/upstream"
	"codearts2api/internal/version"
)

func main() {
	args := os.Args[1:]

	// 版本查询不需要 AK/SK，放在配置校验之前。
	for _, arg := range args {
		if arg == "-version" || arg == "--version" || arg == "-v" {
			fmt.Printf("codearts2api %s\n", version.String())
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	creds := signer.New(cfg.AK, cfg.SK)

	// benefit 子命令走独立流程，不启动 HTTP 服务。
	if len(args) > 0 && args[0] == "benefit" {
		action := "default"
		if len(args) > 1 {
			action = args[1]
		}
		os.Exit(benefit.Run(cfg, creds, action))
	}
	runServer(cfg, creds)
}

func runServer(cfg *config.Config, creds *signer.Credentials) {
	// 注入签名 host 解析：通过中转访问时签名里的 host 必须写上游真实域名，
	// 否则上游网关验签失败（401 / APIG.0301）。未配置时返回空串，
	// 客户端会退回"用请求 URL 的 host 签名"的默认行为。
	newClient := func() *upstream.Client {
		return upstream.New(creds).WithSignedHostResolver(cfg.Endpoints.SignedHostFor)
	}

	// 启动时先同步一次真实模型列表（失败则回退缓存/兜底），再打印。
	client := newClient()
	cat := catalog.New(client, cfg.ModelCachePath, cfg.Endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if !cat.Refresh(ctx) {
		cat.LoadCache()
	}
	cancel()

	srv := server.New(cfg, cat, client)

	// 每天首个请求顺带签到一次，避免常驻定时器的开销。
	var claimer *benefit.AutoClaimer
	if cfg.AutoClaim {
		claimer = benefit.NewAutoClaimer(
			benefit.New(creds, cfg.Endpoints),
			cfg.BenefitStatePath,
			func(format string, args ...any) {
				fmt.Printf("[自动签到] "+format+"\n", args...)
			},
		)
		srv.SetAutoClaim(claimer.MaybeClaim)
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	fmt.Printf("CodeArts OpenAI 兼容代理 %s 已启动: http://%s\n", version.Short(), cfg.Addr())
	fmt.Printf("OpenAI Base URL: %s\n", cfg.BaseURL())
	fmt.Println("可用模型:", strings.Join(cat.IDs(), ", "))
	if claimer != nil {
		fmt.Println("自动签到:", claimer.Describe())
	} else {
		fmt.Println("自动签到: 已关闭（CODEARTS_AUTO_CLAIM=0）")
	}
	if len(cfg.ProxyKeys) > 0 {
		fmt.Printf("下游鉴权: 已开启（%d 个密钥）\n", len(cfg.ProxyKeys))
	} else {
		fmt.Println("下游鉴权: 未开启（任何可访问本端口的人都能使用你的额度）")
	}
	fmt.Println("按 Ctrl+C 停止")

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	<-shutdown
	fmt.Println("\n正在停止...")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = httpServer.Shutdown(stopCtx)
}
