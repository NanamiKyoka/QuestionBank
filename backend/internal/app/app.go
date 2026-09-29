// Package app 组装并运行 QuestionMind 服务。
//
// 抽出来的目的是让两种交付形态共用同一套装配逻辑：
//   - cmd/server   服务器版：监听固定端口，日志打到 stdout
//   - cmd/desktop  桌面版：自动选端口、拉起浏览器、带系统托盘
//
// 如果让桌面版自己再写一遍装配，两边迟早会漂移——
// "服务器上有这个功能、桌面版没有"这种问题排查起来非常费劲。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/db"
	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
	"github.com/NanamiKyoka/questionmind/internal/pkg/cryptox"
	"github.com/NanamiKyoka/questionmind/internal/pkg/jwtx"
	"github.com/NanamiKyoka/questionmind/internal/router"
	"github.com/NanamiKyoka/questionmind/internal/service"
	"github.com/NanamiKyoka/questionmind/internal/version"
	"github.com/NanamiKyoka/questionmind/internal/web"
)

// App 一个已装配好、尚未开始监听的实例。
type App struct {
	Cfg    *config.Config
	Engine http.Handler
	// DataDir 运行期可写目录，桌面版用它实现"打开数据目录"。
	DataDir string

	cleanups []func()
}

// Options 装配参数。
type Options struct {
	Cfg *config.Config
	// Addr 覆盖监听地址。为空时用配置里的地址。
	//
	// 桌面版需要它：固定端口可能被占用，桌面版会先探一个空闲端口再传进来。
	Addr string
	// Logger 输出目标。为空时写 stdout。
	Logger io.Writer
}

// Build 装配全部依赖。调用方必须在结束时调用返回值的 Close。
func Build(ctx context.Context, opt Options) (*App, error) {
	cfg := opt.Cfg
	if cfg == nil {
		return nil, errors.New("app: 缺少配置")
	}

	logger, err := SetupLogger(cfg, opt.Logger)
	if err != nil {
		return nil, err
	}

	info := version.Get()
	slog.Info("QuestionMind 启动中",
		"version", info.Version,
		"commit", info.Commit,
		"go", info.GoVersion,
		"addr", cfg.Addr(),
		"data_dir", cfg.App.DataDir,
		"database", cfg.Database.Driver,
		"ui_embedded", web.Available(),
	)

	gdb, err := db.Open(cfg)
	if err != nil {
		return nil, err
	}

	app := &App{Cfg: cfg, DataDir: cfg.App.DataDir}
	app.cleanups = append(app.cleanups, func() { _ = db.Close(gdb) })
	app.cleanups = append(app.cleanups, logger)

	if cfg.Database.AutoMigrate {
		if err := db.Migrate(gdb); err != nil {
			app.Close()
			return nil, err
		}
	}

	repos := service.NewRepos(gdb)
	tokens := jwtx.NewManager(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.AccessTTL)

	// 字段加密器用 JWT 主密钥派生，因此不需要额外配置项；
	// 但它对密钥做了不同的盐，两者不会互相削弱。
	cipher, err := cryptox.NewCipher(cfg.JWT.Secret)
	if err != nil {
		app.Close()
		return nil, err
	}

	state := service.NewSetupState()
	if err := state.Refresh(ctx, repos.Users); err != nil {
		// 判定失败不阻断启动：中间件会保守地按"未初始化"处理。
		slog.Warn("初始化状态判定失败", "error", err)
	}
	if state.Required() {
		slog.Warn("系统尚未初始化，所有业务接口将返回 503，请访问安装向导")
	}

	activitySvc := service.NewActivityService(repos)
	activitySvc.Start(ctx)

	authSvc := service.NewAuthService(repos, tokens, cfg.App.Name, state)
	aiConfigSvc := service.NewAIConfigService(repos, cipher,
		cfg.AI.RequestTimeout, cfg.AI.MaxRetries)
	questionSvc := service.NewQuestionService(repos)
	vectorSvc := service.NewVectorService(repos, aiConfigSvc)
	kpSvc := service.NewKnowledgePointService(repos)

	// 知识点变更后同步向量索引。索引失败不影响知识点本身——
	// 用户后续可以整体重建索引来补上。
	kpSvc.SetIndexHook(func(subjectID uint, kpIDs []uint) {
		for _, id := range kpIDs {
			vectorSvc.IndexKnowledgePoint(context.Background(), subjectID, id)
		}
	})
	kpSvc.SetDeleteHook(func(kpIDs []uint) {
		vectorSvc.DeleteKnowledgePointVectors(context.Background(), kpIDs)
	})

	// 媒体存储既给导出用（把富文本里的图片地址解析成字节），
	// 也给上传用（落盘并返回对外地址）。
	mediaStore := storage.NewLocal(cfg.Storage.MediaDir, cfg.Storage.PublicPrefix)

	app.Engine = router.New(router.Deps{
		Config:          cfg,
		DB:              gdb,
		Tokens:          tokens,
		Auth:            authSvc,
		Subjects:        service.NewSubjectService(repos),
		KnowledgePoints: kpSvc,
		Tags:            service.NewTagService(repos),
		Questions:       questionSvc,
		Users:           service.NewUserService(repos),
		Papers:          service.NewPaperService(repos),
		Exports:         service.NewExportService(repos, mediaStore),
		AIConfig:        aiConfigSvc,
		Imports:         service.NewImportService(repos, aiConfigSvc, questionSvc).WithVectors(vectorSvc),
		Chat: service.NewChatService(repos, aiConfigSvc, questionSvc,
			kpSvc,
			service.NewTagService(repos),
			service.NewPaperService(repos),
			aiagent.NewChannel(0),
			aiagent.Budget{
				MaxTurns:     cfg.AI.MaxTurns,
				MaxToolCalls: cfg.AI.MaxToolCalls,
				WallClock:    cfg.AI.WallClock,
			}),
		Vectors:    vectorSvc,
		Stats:      service.NewStatsService(repos),
		Activities: activitySvc,
		Settings:   service.NewSettingsService(repos),
		// 提示词服务在构造时把自己注册为 importing 的模板来源，
		// 因此"接口通"与"提示词真的读得到"不可能脱节。
		Prompts: service.NewPromptService(repos),
		Uploads: service.NewUploadService(mediaStore, cfg.Storage.MaxUploadMB),
		Media:   mediaStore,
	})

	if opt.Addr != "" {
		cfg.Server.Host, cfg.Server.Port = splitAddr(opt.Addr, cfg.Server.Port)
	}

	return app, nil
}

// NewServer 构造 HTTP 服务器。超时按 SSE 长连接的需要设置。
func (a *App) NewServer() *http.Server {
	return &http.Server{
		Addr:    a.Cfg.Addr(),
		Handler: a.Engine,
		// 读超时保护慢速请求头攻击；写超时留空，因为 AI 对话使用 SSE 长连接。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Serve 监听并服务，直到 ctx 取消。
//
// 返回 nil 表示正常退出（ctx 取消后优雅关闭完成）。
func (a *App) Serve(ctx context.Context) error {
	srv := a.NewServer()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP 服务已就绪", "addr", srv.Addr, "url", a.URL())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

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

// URL 返回可访问的地址。
//
// 监听 0.0.0.0 时不能把它当作访问地址——浏览器打不开 0.0.0.0，
// 这里有意识地换成 127.0.0.1。
func (a *App) URL() string {
	host := a.Cfg.Server.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, a.Cfg.Server.Port)
}

// Close 释放资源。可重复调用。
func (a *App) Close() {
	for i := len(a.cleanups) - 1; i >= 0; i-- {
		a.cleanups[i]()
	}
	a.cleanups = nil
}

// SetupLogger 配置结构化日志。配置了 log.file 时同时写入文件和 extra。
func SetupLogger(cfg *config.Config, extra io.Writer) (func(), error) {
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

	writers := []io.Writer{os.Stdout}
	if extra != nil {
		writers = append(writers, extra)
	}

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

// FindFreePort 从 prefer 开始找一个可用的端口。
//
// 桌面版必须这样选端口：用户机器上 8080 被占是常事，
// 用固定端口会让应用启动失败，而用户根本不知道该怎么改。
// 从偏好端口开始尝试，尽量保持地址稳定（书签、已登录的浏览器会话都依赖它）。
func FindFreePort(prefer, attempts int) (int, error) {
	if attempts <= 0 {
		attempts = 20
	}
	if prefer <= 0 {
		prefer = 8080
	}

	var lastErr error
	for port := prefer; port < prefer+attempts; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			lastErr = err
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("在 %d-%d 范围内找不到可用端口: %w", prefer, prefer+attempts-1, lastErr)
}

// splitAddr 把 "host:port" 拆开。
func splitAddr(addr string, fallbackPort int) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, fallbackPort
	}
	port := fallbackPort
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		port = fallbackPort
	}
	return host, port
}
