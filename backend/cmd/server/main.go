// Command server 启动 QuestionMind HTTP 服务。
//
// 服务器版：监听配置里的固定地址，日志写 stdout 与日志文件，
// 收到 SIGINT / SIGTERM 后优雅关闭。桌面版见 cmd/desktop。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/NanamiKyoka/questionmind/internal/app"
	"github.com/NanamiKyoka/questionmind/internal/config"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径（默认 config.yaml）")
	addr := flag.String("addr", "", "监听地址，如 127.0.0.1:8080（覆盖配置文件）")
	flag.Parse()

	if err := run(*configPath, *addr); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, addr string) error {
	cfg, err := config.LoadOverrides(configPath, map[string]string{"server.addr": addr})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	instance, err := app.Build(ctx, app.Options{Cfg: cfg, Addr: addr})
	if err != nil {
		return err
	}
	defer instance.Close()

	return instance.Serve(ctx)
}
