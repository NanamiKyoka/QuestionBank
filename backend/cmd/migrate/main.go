// Command migrate 执行与查看数据库迁移。
//
//	migrate up            执行全部未应用的迁移
//	migrate up 3          执行到第 3 版
//	migrate status        列出各版本的执行状态
//	migrate down 1        回滚最近 1 条（基线不可回滚）
//
// 配置来源与服务器一致：环境变量 > config.yaml > 内置默认值，
// 因此不需要为了跑迁移而另备一套连接参数。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/db"
	"github.com/NanamiKyoka/questionmind/internal/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "迁移失败:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径（默认 config.yaml）")
	to := fs.Int("to", 0, "执行到指定版本（0 表示全部）")
	steps := fs.Int("steps", 1, "down 时的回滚条数")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "用法：migrate <up|status|down> [参数]")
		fmt.Fprintln(fs.Output(), "  up      执行全部未应用的迁移（-to 指定目标版本）")
		fmt.Fprintln(fs.Output(), "  status  列出各版本状态")
		fmt.Fprintln(fs.Output(), "  down    回滚最近若干条（-steps，默认 1）")
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	cmd := "up"
	if fs.NArg() > 0 {
		cmd = fs.Arg(0)
		// 允许 `migrate up 3` 这种把版本号当位置参数的写法。
		if n, err := strconv.Atoi(fs.Arg(1)); err == nil {
			*to = n
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	gdb, err := db.Open(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close(gdb) }()

	runner, err := migrations.NewRunner(gdb)
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch cmd {
	case "up":
		applied, err := runner.Up(ctx, *to)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			cur, _ := runner.Current(ctx)
			fmt.Printf("已是最新（当前版本 %d）\n", cur)
			return nil
		}
		for _, m := range applied {
			fmt.Printf("已应用 %s  %s\n", m.Label(), m.Body)
		}
		return nil

	case "status":
		sts, err := runner.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%-6s %-28s %-10s %s\n", "版本", "名字", "状态", "执行时间")
		for _, s := range sts {
			state := "未执行"
			switch {
			case s.Dirty:
				state = "不匹配!"
			case s.Applied:
				state = "已应用"
			}
			ts := ""
			if s.Applied {
				ts = s.AppliedAt.Local().Format(time.RFC3339)
			}
			fmt.Printf("%04d   %-28s %-10s %s\n", s.Version, s.Name, state, ts)
		}
		// status 也必须暴露校验和不一致，否则用户看到"一切正常"却无法启动。
		if err := runner.Verify(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "\n警告：", err)
			return err
		}
		return nil

	case "down":
		if err := runner.Down(ctx, *steps); err != nil {
			return err
		}
		cur, _ := runner.Current(ctx)
		fmt.Printf("已回滚，当前版本 %d\n", cur)
		return nil
	}

	fs.Usage()
	return fmt.Errorf("未知子命令：%s", cmd)
}
