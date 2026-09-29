// Command desktop 以桌面应用的方式启动 QuestionMind。
//
// 与服务器版的区别：
//   - 自动挑选空闲端口（用户机器上 8080 被占是常事）
//   - 启动后自动打开浏览器
//   - 数据默认放在用户目录下的 .questionmind，而不是当前工作目录
//   - 控制台保持前台运行，Ctrl+C 优雅退出
//
// 它仍然是**同一个单文件二进制**：前端产物经 embed 打进可执行文件，
// 不依赖任何外部服务或运行时。之所以不做成 Tauri 那样的原生窗口，
// 是因为那需要 WebView2 运行时并按平台分别构建；用系统浏览器打开
// 本地服务，部署成本最低而功能完全一致。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/NanamiKyoka/questionmind/internal/app"
	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/web"
)

func main() {
	var (
		port      = flag.Int("port", 8080, "首选端口；被占用时自动向后寻找空闲端口")
		openUI    = flag.Bool("open", true, "启动后自动打开浏览器")
		dataDir   = flag.String("data", "", "数据目录（默认为用户目录下的 .questionmind）")
		configPtr = flag.String("config", "", "配置文件路径")
	)
	flag.Parse()

	if err := run(*port, *openUI, *dataDir, *configPtr); err != nil {
		fmt.Fprintf(os.Stderr, "\n启动失败: %v\n", err)
		fmt.Fprintln(os.Stderr, "按回车键退出…")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
}

func run(preferPort int, openUI bool, dataDir, configPath string) error {
	banner()

	// 数据目录：默认放在用户目录下，而不是当前工作目录。
	// 桌面应用的工作目录取决于快捷方式是怎么创建的，放那里会让
	// "数据库到底在哪"变成一个需要现场排查的问题。
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("无法确定用户目录: %w", err)
		}
		dataDir = filepath.Join(home, ".questionmind")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("解析数据目录失败: %w", err)
	}
	dataDir = abs

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	// 配置里的相对路径基于数据目录解析，因此必须在 Load 之前设置。
	if err := os.Setenv("QM_DATA_DIR", dataDir); err != nil {
		return fmt.Errorf("设置数据目录失败: %w", err)
	}

	port, err := app.FindFreePort(preferPort, 20)
	if err != nil {
		return err
	}
	if port != preferPort {
		fmt.Printf("端口 %d 已被占用，改用 %d\n\n", preferPort, port)
	}

	cfg, err := config.LoadOverrides(configPath, map[string]string{
		"server.host": "127.0.0.1",
		"server.port": fmt.Sprintf("%d", port),
		// 桌面版只监听本机，不需要对外暴露也不需要 CORS。
		"server.mode": "release",
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	instance, err := app.Build(ctx, app.Options{Cfg: cfg})
	if err != nil {
		return err
	}
	defer instance.Close()

	url := instance.URL()

	if !web.Available() {
		fmt.Println("提示：本次构建没有内嵌前端产物，只有 API 可用。")
		fmt.Println("      请先执行 make web（或 make build）重新构建。")
		fmt.Println()
	}

	fmt.Printf("  访问地址  %s\n", url)
	fmt.Printf("  数据目录  %s\n", dataDir)
	fmt.Println()
	fmt.Println("  按 Ctrl+C 退出")
	fmt.Println()

	if openUI {
		// 打开浏览器失败不该让服务起不来：用户可以自己复制地址。
		if err := openBrowser(url); err != nil {
			fmt.Printf("  无法自动打开浏览器（%v），请手动访问上面的地址\n\n", err)
		}
	}

	return instance.Serve(ctx)
}

func banner() {
	fmt.Println()
	fmt.Println("  ╭──────────────────────────────────────────────╮")
	fmt.Println("  │  QuestionMind · AI 原生题库与组卷系统        │")
	fmt.Println("  ╰──────────────────────────────────────────────╯")
	fmt.Println()
}

// openBrowser 用系统默认浏览器打开地址。
//
// 三个平台各有一条命令；不能用 runtime.GOOS 之外的判断，
// 因为交叉编译时 GOOS 是目标平台而不是运行平台——不过
// 桌面版本来就是本机构建，这里不存在交叉编译的场景。
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// 用 rundll32 而不是 cmd /c start：start 会把 & 之类的字符
		// 当成命令分隔符，含查询串的地址会被截断。
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	// 子进程的输出对本程序没有意义，直接丢弃避免污染控制台。
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}
