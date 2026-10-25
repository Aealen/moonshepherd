// shepherd Agent 入口（docs/01 §3）。
//
// 配置来源：命令行参数 > 环境变量。
//
//	HERD_SERVER   面板地址（wss://panel.example.com 或调试 ws://127.0.0.1:8080）
//	HERD_TOKEN    一次性注册 Token（仅首次注册需要；之后凭据存于 --cred-file）
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/maxonphong/herd/internal/agentd"
	"github.com/maxonphong/herd/version"
)

func main() {
	fs := flag.NewFlagSet("shepherd", flag.ExitOnError)
	server := fs.String("server", os.Getenv("HERD_SERVER"), "面板地址（wss://…）")
	token := fs.String("token", os.Getenv("HERD_TOKEN"), "一次性注册 Token（首连注册用）")
	credFile := fs.String("cred-file", os.Getenv("HERD_CRED_FILE"), "凭据文件路径（默认 ~/.herd/credentials.json）")
	verbose := fs.Bool("v", false, "调试日志")
	_ = fs.Parse(os.Args[1:])

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("shepherd", version.Version)
		return
	}
	if *server == "" {
		fmt.Fprintln(os.Stderr, "缺少面板地址：--server 或 HERD_SERVER（wss://…）")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agent := agentd.New(agentd.Options{
		Server: *server, Token: *token, CredFile: *credFile,
	})
	if err := agent.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("Agent 退出", "err", err)
		os.Exit(1)
	}
	slog.Info("Agent 已停止")
}
