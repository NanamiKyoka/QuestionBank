// Command server 启动 QuestionMind HTTP 服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/db"
	"github.com/NanamiKyoka/questionmind/internal/pkg/jwtx"
	"github.com/NanamiKyoka/questionmind/internal/router"
	"github.com/NanamiKyoka/questionmind/internal/service"
	"github.com/NanamiKyoka/questionmind/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "配置文件路径（默认 config.yaml）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	closeLog, err := setupLogger(cfg)
	if err != nil {
		return err
	}
	defer closeLog()

	info := version.Get()
	slog.Info("QuestionMind 启动中",
		"version", info.Version,
		"commit", info.Commit,
		"go", info.GoVersion,
		"config", cfg.Addr(),
		"data_dir", cfg.App.DataDir,
		"database", cfg.Database.Driver,
	)

	gdb, err := db.Open(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close(gdb) }()

	if cfg.Database.AutoMigrate {
		if err := db.AutoMigrate(gdb); err != nil {
			return err
		}
	}

	repos := service.NewRepos(gdb)
	tokens := jwtx.NewManager(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.AccessTTL)

	state := service.NewSetupState()
	if err := state.Refresh(context.Background(), repos.Users); err != nil {
		// 判定失败不阻断启动：中间件会保守地按"未初始化"处理。
		slog.Warn("初始化状态判定失败", "error", err)
	}
	if state.Required() {
		slog.Warn("系统尚未初始化，所有业务接口将返回 503，请访问安装向导")
	}

	authSvc := service.NewAuthService(repos, tokens, cfg.App.Name, state)

	engine := router.New(router.Deps{
		Config: cfg,
		DB:     gdb,
		Auth:   authSvc,
		Tokens: tokens,
	})

	srv := &http.Server{
		Addr:    cfg.Addr(),
		Handler: engine,
		// 读超时保护慢速请求头攻击；写超时留空，因为 AI 对话使用 SSE 长连接。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP 服务已就绪", "addr", cfg.Addr())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-ctx.Done():
		slog.Info("收到退出信号，正在优雅关闭…")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭失败: %w", err)
	}
	slog.Info("已退出")
	return nil
}

// setupLogger 配置结构化日志。配置了 log.file 时同时写入文件和控制台。
func setupLogger(cfg *config.Config) (func(), error) {
	var level slog.Level
	switch cfg.Log.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var writers []io.Writer
	writers = append(writers, os.Stdout)

	var file *os.File
	if cfg.Log.File != "" {
		f, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
		file = f
		writers = append(writers, f)
	}

	handler := slog.NewJSONHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	return func() {
		if file != nil {
			_ = file.Close()
		}
	}, nil
}
