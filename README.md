# 牧云 MoonShepherd

> 把散落在云端的服务器，当羊群来放牧。

**牧云（MoonShepherd）** 是一个自建的跨服务器统一管理后台：一个 Web 面板遥控 + 感知一群性格迥异的 Linux 节点——温顺的标准云主机，或者 Unraid 这种只有 BusyBox、没有 systemd 的刺头。

## 核心能力

- **节点纳管**：受控端 Agent 以 Docker 容器或静态二进制等多种形态部署，主动外连面板，无需开放入站端口
- **应用感知**：探测器定期检测节点上已安装的应用（Docker 容器、Nginx、Caddy 等）并上报面板
- **远程执行**：跨节点命令执行，全程审计
- **配置管理**：Nginx/Caddy 等应用配置的版本化下发，自带校验与失败自动回滚
- **渐进扩展**：每种应用支持 = 一个探测器插件，逐个增加

## 设计文档

| 文档 | 内容 |
|---|---|
| [01-产品总体设计](docs/01-产品总体设计.md) | 架构、Agent/面板设计、通信协议、数据模型、配置管理流程、安全设计 |
| [02-技术选型与工程结构](docs/02-技术选型与工程结构.md) | 技术栈定稿与理由、仓库结构、构建与分发 |
| [03-迭代路线图](docs/03-迭代路线图.md) | MVP 边界、P0/P1/P2 迭代计划、里程碑、风险与待定项 |
| [04-通信协议规范](docs/04-通信协议规范.md) | 节点身份认证（HMAC 挑战）、WSS 消息字段级定义、指令幂等与重连语义 |
| [05-安全设计](docs/05-安全设计.md) | 公网产品级威胁模型、分层防御、公网部署 checklist |
| [06-面板API与页面设计](docs/06-面板API与页面设计.md) | REST API 端点、浏览器实时事件通道、前端页面结构与交互规范 |

## 部署形态一览

```
                ┌─────────────────────────┐
                │   牧云面板（单二进制或容器）  │
                │   部署在任意合适的位置       │
                └───────────△─────────────┘
                            │ Agent 主动 WSS 外连（免入站端口）
        ┌───────────────────┴──────────────────┐
        │                                      │
  节点A Unraid                            节点B 阿里云ECS
  Agent=Docker容器(挂docker.sock)          Agent=静态二进制+systemd
  (备选:裸二进制+UserScripts)              (install.sh 一条命令)
```

## 部署

### Docker（推荐，CI 自动构建）

push 到 GitHub 后 CI 自动测试并构建双镜像（amd64/arm64）推 GHCR：

```bash
# 面板（任意 Docker 机 / NAS）
docker run -d --name herd --restart unless-stopped \
  -p 8080:8080 -v ./data:/app/data \
  ghcr.io/aealen/moonshepherd:latest

# Agent（Unraid 等；面板发放 Token 后）
docker run -d --name herd --restart unless-stopped \
  -e HERD_SERVER=wss://panel.example.com \
  -e HERD_TOKEN=rt_xxx \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /etc/nginx:/etc/nginx \
  -v herd-data:/data \
  ghcr.io/aealen/herd:latest
```

首次启动日志打印随机 admin 密码（或 `-e HERD_ADMIN_PASSWORD=...` 指定）。公网部署必须 TLS（前置反代或挂载证书，见 [docs/05-安全设计](docs/05-安全设计.md)）。

也可用仓库内 `packaging/docker-compose.yml` 本地构建部署。

### 二进制

`make cross` 产出 Linux amd64/arm64 二进制（Agent 另有 armv7），配 systemd 或 User Scripts 运行。

## 快速开始（开发）

```bash
# 面板（开发模式，数据在 ./data；首次启动打印随机管理员密码）
make dev            # = 前端 vite 热更(:5173) + 面板(:8080)
# 或只起面板：go run ./cmd/moon server --dev --data ./data

# Agent（先用面板发放注册 Token，然后）
HERD_SERVER=ws://127.0.0.1:8080 HERD_TOKEN=rt_xxx go run ./cmd/herd -v

# 构建与测试
make test           # go test ./...
make backend        # dist/moon + dist/herd（本机平台）
make cross          # Linux amd64/arm64 + herd armv7
```

公网部署：前置反代终结 TLS（推荐）或 `--tls-cert/--tls-key`；详见 [docs/05-安全设计](docs/05-安全设计.md)。

## 状态

M1 完成（协议 + WSS 网关 + 注册/认证/心跳 + 面板/Agent 骨架 + 前端七页），测试全绿。
语言：中文 / 产品代号：MoonShepherd / 技术栈：Go + Vue 3
