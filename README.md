<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo-dark.png">
  <img src="docs/brand/logo-light.png" width="88" alt="牧云 MoonShepherd">
</picture>

# 牧云 MoonShepherd

**把散落在云端的服务器，当羊群来放牧。**

自托管的跨服务器统一管理面板：一个 Web 界面遥控并感知一群性格迥异的 Linux 节点——温顺的标准云主机，或者 Unraid 这种只有 BusyBox、没有 systemd 的刺头。

[![CI](https://github.com/Aealen/moonshepherd/actions/workflows/ci.yml/badge.svg)](https://github.com/Aealen/moonshepherd/actions/workflows/ci.yml)
![release](https://img.shields.io/badge/release-v0.1.0-63d3a6)
![platform](https://img.shields.io/badge/platform-linux%20amd64%20%7C%20arm64%20%7C%20armv7-2f343c)
![stack](https://img.shields.io/badge/stack-Go%20%2B%20Vue%203-2f343c)

[功能总览](#-功能总览) · [快速开始](#-快速开始) · [AI 客户端接入](#-让-ai-替你放牧mcp) · [设计文档](#-设计文档)

</div>

---

| 主面板 · 节点总览 | 登录页 |
|---|---|
| ![主面板](docs/screenshots/dashboard.png) | ![登录页](docs/screenshots/login.png) |

## 🗺️ 放牧场景

| 场景 | 牧云怎么帮你 |
|---|---|
| 🏠 **家庭 NAS / Unraid** | BusyBox 刺头也能管——裸二进制宿主机直跑 + User Scripts 自启，Docker 探测零配置直连、进程级感知完整，不依赖 systemd |
| ☁️ **公网云主机** | 一条 `install.sh` 接入；Agent 只出不进、不开入站端口，面板侧前置 TLS 即可公网运营 |
| 🏢 **多机房 / 异地节点** | 所有节点一块看板：心跳、指标、应用清单实时汇聚，跨节点指令与文件流转全程审计 |
| 💻 **本地开发机** | Web 终端直达 PTY，浏览器里看日志、改配置、跑命令，不用来回切窗口 |

## 🐑 功能总览

### 节点牧群
- **主动外连纳管**——Agent 以 Docker 容器 / 静态二进制 / systemd 等多种形态部署，主动 WSS 外连面板，**无需开放节点入站端口**；HMAC 挑战式认证
- **心跳与指标**——CPU / 内存 / 磁盘 / 网络实时心跳，在线/离线状态即时刷新；节点总览顶部还有**全牧群网络吞吐曲线**（近 1 小时跨节点聚合，悬停逐桶读数）
- **一键接入**——面板发放注册 Token 后，目标节点上一条 `install.sh` 完成安装注册；Agent 自带**自更新**：旁路写入不动旧二进制、失败自动回滚、systemd / 裸跑形态自适应，防失联三重保障

### 应用感知与容器
- **探测器插件**——自动发现节点上的应用：Docker 容器、Nginx、Caddy、systemd 服务，插件化渐进扩展
- **应用详情**——镜像 / 端口映射 / 目录挂载 / 网络与环境变量；Docker 应用自动识别**启动方式（Docker Compose 还是 Docker Run）**并展示 ENTRYPOINT / CMD / 重启策略
- **容器操作**——启动 / 停止 / 重启 / 拉取镜像（白名单，无删除），**容器日志实时流**回浏览器
- **配置发现与纳管**——Nginx / Caddy 配置文件自动探测，纳管后在线编辑、版本化下发，自带校验与失败自动回滚

![应用中心](docs/screenshots/apps.png)

### 运维与安全
- **Web 终端**——浏览器直达节点 PTY，vi / top 全可用；连接计时、一键全屏（Esc 退出）、常用命令速插条（只插入不执行）、会话溯源水印，输入全程审计
- **指令中心**——跨节点远程命令执行，输出可查，全程审计
- **文件管理器**——目录树浏览、分片上传下载、在线编辑、重命名 / 移动 / 删除，全部变更留痕
- **操作审计**——只读、不可删除的全量操作审计
- **公网级安全**——TLS 强制、分层威胁模型、最小权限 MCP 授权（见 [安全设计](docs/05-安全设计.md)）

![指令中心](docs/screenshots/commands.png)

### 🤖 让 AI 替你放牧（MCP）
内置 **MCP Server（Streamable HTTP）**，Claude Desktop 等 AI 客户端可安全操控牧云：

- **12 个工具**：节点列表 / 指标查询、应用清单、容器日志、容器操作、远程命令、文件读写……
- **两级授权**：能力（scope，决定"能做什么"）× 范围（节点 → 应用，决定"对哪些对象做"），随时编辑即时生效
- **Key 管理**——面板创建 / 启停 / 吊销，高危能力（命令执行）创建时二次确认
- **全程审计**——授权范围外的请求被拒绝并记录

![MCP Keys](docs/screenshots/mcp.png)

## 🚀 快速开始

### 面板（任意 Docker 机 / NAS）

```bash
docker run -d --name herd --restart unless-stopped \
  -p 8080:8080 -v ./data:/app/data \
  ghcr.io/aealen/moonshepherd:latest
```

首次启动日志打印随机 admin 密码（或 `-e HERD_ADMIN_PASSWORD=...` 指定）。CI 自动构建 amd64 / arm64 双镜像推 GHCR，也支持 `packaging/docker-compose.yml` 本地编排。

### Agent 接入节点

面板「节点注册」发放 Token 后，在目标节点上执行（脚本由面板动态渲染，自动适配面板地址与系统架构）：

```bash
curl -fsSL http://<面板地址>/install.sh | bash -s -- --token rt_xxx
```

- **标准 Linux**：安装为 systemd 服务
- **Unraid 等 BusyBox 环境**：裸二进制落 `/mnt/user/herd-workdir`，自动生成 User Scripts 自启脚本（宿主机直跑，Docker 探测与进程感知零额外配置）
- **公网面板**：节点同样只出不进；面板侧请前置 TLS（详见 [安全设计](docs/05-安全设计.md)）

### Agent 自更新

面板「节点」页可一键下发升级（按节点架构自动匹配二进制），Agent 端校验 sha256 → 旁路写入 → 平滑换身 → 失败自动回滚，升级期间不失联。

### 环境要求

| 组件 | 要求 |
|---|---|
| 面板 | 任意能跑 Docker 的 Linux 主机 / NAS（amd64 / arm64），SQLite 内嵌、无外部依赖 |
| Agent | Linux amd64 / arm64 / armv7，宿主机直跑；标准发行版走 systemd，BusyBox 环境（Unraid 等）走裸二进制 + User Scripts 自启 |
| 浏览器 | Chrome / Edge / Firefox / Safari 现代版本 |

## 🤖 AI 客户端接入（MCP）

```text
URL:     https://<面板域名>/mcp
Header:  Authorization: Bearer mcp_xxx…（面板「MCP Keys」创建，仅显示一次）
```

之后对 AI 说"列一下节点""看下 Gitea 容器日志"即可——授权范围外的请求会被拒绝并记录审计。接入示例（Claude Desktop）：

```json
{ "mcpServers": { "moonshepherd": {
    "type": "http", "url": "https://面板地址/mcp",
    "headers": { "Authorization": "Bearer mcp_xxx…" } } } }
```

## 🏗️ 架构

```
                ┌─────────────────────────┐
                │   牧云面板（单二进制或容器）   │
                │   SQLite 存储 · MCP 内嵌    │
                └───────────△─────────────┘
                            │ Agent 主动 WSS 外连（免入站端口）
        ┌───────────────────┴──────────────────┐
        │                                      │
  节点A Unraid                            节点B 阿里云ECS
  Agent=裸二进制(User Scripts自启)          Agent=systemd(install.sh 一键)
  (宿主机直跑,零挂载直连docker.sock)        (备选:裸二进制)
```

## 📚 设计文档

| 文档 | 内容 |
|---|---|
| [01-产品总体设计](docs/01-产品总体设计.md) | 架构、Agent/面板设计、通信协议、数据模型、配置管理流程、安全设计 |
| [02-技术选型与工程结构](docs/02-技术选型与工程结构.md) | 技术栈定稿与理由、仓库结构、构建与分发 |
| [03-迭代路线图](docs/03-迭代路线图.md) | MVP 边界、P0/P1/P2 迭代计划、里程碑、风险与待定项 |
| [04-通信协议规范](docs/04-通信协议规范.md) | 节点身份认证（HMAC 挑战）、WSS 消息字段级定义、指令幂等与重连语义 |
| [05-安全设计](docs/05-安全设计.md) | 公网产品级威胁模型、分层防御、公网部署 checklist |
| [06-面板API与页面设计](docs/06-面板API与页面设计.md) | REST API 端点、浏览器实时事件通道、前端页面结构与交互规范 |
| [07-MCP服务设计](docs/07-MCP服务设计.md) | MCP Server 架构、工具清单、两级授权模型 |
| [07a-MCP实现规范](docs/07a-MCP实现规范.md) | MCP 工具实现细节与校验规则 |
| [08-服务端二进制部署与数据迁移](docs/08-服务端二进制部署与数据迁移.md) | 面板裸机/systemd 部署规范、全参数表、跨机数据迁移与 Agent 切换指向 |

## 🛠️ 本地开发

```bash
make dev            # 前端 vite 热更(:5173) + 面板(:8080)，数据在 ./data
make test           # go test ./...
make backend        # dist/moon + dist/herd（本机平台）
make cross          # Linux amd64/arm64 交叉编译（Agent 另有 armv7）
```

前端技术栈：Vue 3 + Naive UI + Pinia + Vite；面板 Go 内嵌前端产物（go:embed）。

## 📌 状态

**v0.1.0 已发布**（见 Releases / Tags）。协议、终端、容器、文件、MCP、Agent 自更新等核心能力全部落地，全链路审计贯穿；已在内网 NAS（Unraid）与公网云主机上稳定运行。

---

语言：中文 · 产品代号：**MoonShepherd** · 技术栈：Go + Vue 3
