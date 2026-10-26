// install.sh 一键安装与 Agent 二进制下发(docs/01 §4.2 分发通道③:
// 面板自身作为分发源,私有部署零外部依赖)。
//
//	GET /install.sh                  动态渲染安装脚本(面板地址按请求 Host 填充)
//	GET /dist/herd-linux-{arch}      下发内嵌的 Agent 二进制
package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// distArchs 支持下发的架构(docs/02 §1.3 交叉编译矩阵)。
var distArchs = map[string]bool{"amd64": true, "arm64": true, "armv7": true}

// handleDistBinary GET /dist/herd-linux-{arch}。
func (s *Server) handleDistBinary(w http.ResponseWriter, r *http.Request) {
	arch := chi.URLParam(r, "arch")
	if !distArchs[arch] {
		writeErr(w, http.StatusNotFound, "not_found", "不支持的架构: "+arch)
		return
	}
	path := filepath.Join(s.opt.DistDir, "herd-linux-"+arch)
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, "no_binary",
			"此面板构建未内嵌 Agent 二进制(herd-linux-"+arch+");Docker 镜像部署自带,源码部署请先 make distro-bin")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

// handleInstallSh GET /install.sh:渲染安装脚本。
// server/ws 地址按请求 Host 与部署形态(TLS/TrustProxy)推导,
// --server/--token 仍可命令行覆盖。
func (s *Server) handleInstallSh(w http.ResponseWriter, r *http.Request) {
	httpScheme, wsScheme := "https", "wss"
	if !s.isSecureRequest(r) {
		httpScheme, wsScheme = "http", "ws"
	}
	host := r.Host
	if xfp := r.Header.Get("X-Forwarded-Host"); xfp != "" && s.opt.TrustProxy {
		host = strings.TrimSpace(strings.SplitN(xfp, ",", 2)[0])
	}
	script := strings.ReplaceAll(installShTemplate, "__HTTP_BASE__", httpScheme+"://"+host)
	script = strings.ReplaceAll(script, "__WS_SERVER__", wsScheme+"://"+host)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write([]byte(script))
}

// installShTemplate 安装脚本模板(docs/01 §4.2):
// systemd 机装 unit(F1);无 systemd(Unraid 等)落二进制 + 输出自启模板(F3)。
var installShTemplate = `#!/bin/sh
# MoonShepherd Agent(herd)一键安装 —— docs/01 §4.2 F1/F3
# 用法:
#   curl -s __HTTP_BASE__/install.sh | bash -s -- --token rt_xxx
#   可选:--server wss://其他面板   --dir /无 systemd 时的安装目录
set -eu

SERVER="__WS_SERVER__"
TOKEN=""
DIR="/usr/local/bin"
NO_SYSTEMD_DIR="/mnt/user/herd-workdir"

while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER="$2"; shift 2 ;;
    --token)  TOKEN="$2"; shift 2 ;;
    --dir)    NO_SYSTEMD_DIR="$2"; shift 2 ;;
    *) echo "未知参数: $1"; exit 2 ;;
  esac
done

[ "$(id -u)" = 0 ] || { echo "错误:请以 root 运行(安装到系统目录/注册服务)"; exit 1; }
[ -n "$TOKEN" ] || { echo "错误:缺少 --token(面板「节点注册」生成)"; exit 1; }

# 架构映射
case "$(uname -m)" in
  x86_64)         ARCH=amd64 ;;
  aarch64|arm64)  ARCH=arm64 ;;
  armv7l|armv6l)  ARCH=armv7 ;;
  *) echo "错误:不支持的架构 $(uname -m)"; exit 1 ;;
esac

# 面板下载基址:ws(s):// → http(s)://
HTTP_BASE="$(echo "$SERVER" | sed 's/^wss:/https:/; s/^ws:/http:/')"

echo "==> 下载 herd-linux-$ARCH"
curl -fSL "$HTTP_BASE/dist/herd-linux-$ARCH" -o /tmp/herd-install
chmod +x /tmp/herd-install
VERSION="$(/tmp/herd-install version 2>/dev/null || echo unknown)"
echo "==> Agent 版本: $VERSION"

if [ -d /run/systemd/system ]; then
  # F1:systemd 服务
  install -m 0755 /tmp/herd-install "$DIR/herd"
  mkdir -p /var/lib/herd
  cat > /etc/systemd/system/herd.service <<UNIT
[Unit]
Description=MoonShepherd Herd Agent
After=network-online.target
Wants=network-online.target

[Service]
Environment=HERD_SERVER=$SERVER
Environment=HERD_CRED_FILE=/var/lib/herd/credentials.json
Environment=HERD_TOKEN=$TOKEN
ExecStart=$DIR/herd
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now herd
  echo "==> 安装完成:"
  echo "    journalctl -u herd -f        # 查看日志(注册成功即接入)"
  echo "    systemctl restart herd       # 重启"
  echo "    Token 注册成功后自动作废,unit 中残留无安全影响"
else
  # F3:无 systemd(Unraid 等)—— 落二进制 + 输出 User Scripts 自启模板
  install -m 0755 /tmp/herd-install "$NO_SYSTEMD_DIR/herd-linux-amd64" 2>/dev/null \
    || install -m 0755 /tmp/herd-install "$NO_SYSTEMD_DIR/herd"
  cat > "$NO_SYSTEMD_DIR/conn_script.sh" <<SCRIPT
#!/bin/bash
pkill -f herd-linux- 2>/dev/null; sleep 1
HERD_SERVER=$SERVER \\
HERD_TOKEN=$TOKEN \\
HERD_CRED_FILE=$NO_SYSTEMD_DIR/credentials.json \\
nohup $NO_SYSTEMD_DIR/herd-linux-* >> $NO_SYSTEMD_DIR/herd.log 2>&1 &
echo "herd Agent 已启动 (PID \$!)"
SCRIPT
  chmod +x "$NO_SYSTEMD_DIR/conn_script.sh"
  echo "==> 无 systemd,已写入:"
  echo "    $NO_SYSTEMD_DIR/herd-linux-amd64(二进制)"
  echo "    $NO_SYSTEMD_DIR/conn_script.sh(启动脚本,已含 Server/Token)"
  echo "==> 立即启动:bash $NO_SYSTEMD_DIR/conn_script.sh"
  echo "==> 开机自启:User Scripts 插件把该脚本 Schedule 设为 At Startup of Array"
fi
`
