// moon 面板入口（MoonShepherd · 牧云）。
//
// 用法：
//
//	moon server [--data ./data] [--addr :8080] [--tls-cert c --tls-key k] [--dev]
//	            [--admin-password <初始密码>]   # 仅首次初始化管理员时使用
//	moon version
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aealen/moonshepherd/internal/credenc"
	"github.com/aealen/moonshepherd/internal/server"
	"github.com/aealen/moonshepherd/internal/store"
	"github.com/aealen/moonshepherd/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println("moonshepherd", version.Version)
			return
		case "admin":
			adminCmd(os.Args[2:])
			return
		case "server":
			os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		default:
			fmt.Fprintf(os.Stderr, "未知子命令 %q；可用：server / admin / version\n", os.Args[1])
			os.Exit(2)
		}
	} else {
		os.Args = append(os.Args, "server")
	}

	fs := flag.NewFlagSet("server", flag.ExitOnError)
	var (
		dataDir   = fs.String("data", "./data", "数据目录（herd.db + master.key）")
		addr      = fs.String("addr", ":8080", "监听地址")
		tlsCert   = fs.String("tls-cert", "", "TLS 证书文件（公网直跑；建议前置反代，见 docs/05）")
		tlsKey    = fs.String("tls-key", "", "TLS 私钥文件")
		dev       = fs.Bool("dev", false, "开发模式（本地 http：Cookie 不加 Secure、放宽回环限制）")
		adminPass = fs.String("admin-password", "", "首次初始化的管理员密码（缺省则生成随机密码打印一次）")
	)
	_ = fs.Parse(os.Args[1:])

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	st, err := store.Open(*dataDir)
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	box, err := credenc.Open(*dataDir)
	if err != nil {
		slog.Error("初始化凭据加密失败", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := initAdmin(ctx, st, *adminPass); err != nil {
		slog.Error("初始化管理员失败", "err", err)
		os.Exit(1)
	}

	srv, err := server.New(server.Options{
		DataDir: *dataDir, Addr: *addr,
		TLSCertFile: *tlsCert, TLSKeyFile: *tlsKey, Dev: *dev,
	}, st, box)
	if err != nil {
		slog.Error("组装面板失败", "err", err)
		os.Exit(1)
	}
	srv.StartBackground(ctx)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("面板退出", "err", err)
		os.Exit(1)
	}
}

// initAdmin 首次启动初始化管理员：--admin-password > HERD_ADMIN_PASSWORD > 随机生成打印一次。
func initAdmin(ctx context.Context, st *store.Store, flagPass string) error {
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	pass := flagPass
	if pass == "" {
		pass = os.Getenv("HERD_ADMIN_PASSWORD")
	}
	if pass == "" {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		pass = hex.EncodeToString(b)
		slog.Warn("首次启动：已创建管理员 admin，随机密码如下（仅显示这一次，请立即登录修改）")
		fmt.Println("  用户名: admin")
		fmt.Println("  密码:   " + pass)
	} else {
		slog.Info("首次启动：已创建管理员 admin（密码来自参数/环境变量）")
	}
	if len(pass) < 10 {
		return fmt.Errorf("管理员密码最短 10 位（docs/05 §3.2）")
	}
	return st.CreateUser(ctx, "admin", pass)
}

// adminCmd moon admin 子命令：reset-password（忘记密码时的运维入口）。
func adminCmd(args []string) {
	fs := flag.NewFlagSet("admin reset-password", flag.ExitOnError)
	dataDir := fs.String("data", "./data", "数据目录")
	user := fs.String("user", "admin", "用户名")
	newPass := fs.String("new-password", "", "新密码（缺省则生成随机密码打印）")
	_ = fs.Parse(args)
	if fs.NArg() > 0 && fs.Arg(0) != "reset-password" {
		fmt.Fprintf(os.Stderr, "用法：moon admin reset-password [--data dir] [--user name] [--new-password pwd]\n")
		os.Exit(2)
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	ctx := context.Background()
	u, err := st.GetUser(ctx, *user)
	if err != nil || u == nil {
		slog.Error("用户不存在", "user", *user)
		os.Exit(1)
	}
	pass := *newPass
	if pass == "" {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		pass = hex.EncodeToString(b)
	}
	if len(pass) < 10 {
		slog.Error("密码最短 10 位（docs/05 §3.2）")
		os.Exit(1)
	}
	if err := st.UpdatePassword(ctx, u.ID, pass); err != nil {
		slog.Error("重置失败", "err", err)
		os.Exit(1)
	}
	st.AddAudit(ctx, u.ID, u.Username, "auth.password_reset", u.Username, "", "localhost")
	slog.Info("密码已重置；面板内存会话不包含本操作，全部既有会话需重新登录")
	fmt.Println("  用户名: " + *user)
	fmt.Println("  新密码: " + pass)
}
