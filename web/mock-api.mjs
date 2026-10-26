// 开发环境 mock 插件:拦截 /api/v1 返回仿真牧场数据(仅 vite dev,不进构建产物)。
// 用途:本地审美走查——节点/应用/指令/审计全链路有料可看。
// 启用:vite.config.ts 里 plugins 挂 mockPlugin();不挂即完全关闭。
import { randomUUID } from 'node:crypto'

// ---------- 数据工厂(模块级单例,进程存活期间状态自洽) ----------

const now = () => Math.floor(Date.now() / 1000)
const start = now()

function mkNode(id, name, hostname, os, osVer, arch, status, enabled = true) {
  return {
    id, name, hostname, os_name: os, os_version: osVer, os_arch: arch,
    agent_version: 'main-d70fa47', proto_version: '1.0',
    status, enabled, last_seen: now() - (status === 'online' ? 8 : 3600 * 5),
    tags: id === 'n_light' ? ['home', 'nas'] : id === 'n_aliyun' ? ['cloud', 'web'] : ['office'],
    capabilities: status === 'online' ? ['probe', 'exec', 'pty', 'docker'] : ['probe', 'exec'],
    created_at: start - 86400 * 30,
  }
}

const nodes = [
  mkNode('n_light', '灯塔·Light-Tower', 'Light-Tower', 'Slackware', '14.2', 'amd64', 'online'),
  mkNode('n_aliyun', '阿里云·杭州/web-01', 'iZbp1dbljj5cgoshjm7xdjZ', 'Alibaba Cloud Linux', '3', 'amd64', 'online'),
  mkNode('n_office', '办公室演示机', 'DESKTOP-DEMO01', 'Ubuntu', '24.04', 'amd64', 'offline'),
]

// 心跳历史:在线节点 60 个点,cpu/mem 有波形
function mkMetrics(nodeID, base) {
  return Array.from({ length: 60 }, (_, i) => ({
    node_id: nodeID, ts: now() - (59 - i) * 30,
    cpu: Math.max(1, Math.min(97, base + Math.round(28 * Math.sin(i / 5) + (i * 13) % 17))),
    mem_used: 3800 + (i * 37) % 900, mem_total: 8192,
    disk_used: 210 + (i % 3), disk_total: 500,
    net_rx: 120 + (i * 29) % 800, net_tx: 60 + (i * 17) % 400, load1: 0.4 + (i % 9) / 10,
  }))
}
const metrics = [...mkMetrics('n_light', 22), ...mkMetrics('n_aliyun', 48)]

// 应用:两节点共 9 个
const apps = [
  { id: 1, node_id: 'n_light', node_name: '灯塔·Light-Tower', probe_name: 'docker', app_name: 'Gitea', app_type: 'docker', version: 'gitea/gitea:1.22', status: 'running', detail: { image: 'gitea/gitea:latest', ports: [{ ip: '0.0.0.0', public: 7111, private: 3000, type: 'tcp' }], id: 'a1f4c9e2b7' }, detected_at: now() - 60 },
  { id: 2, node_id: 'n_light', node_name: '灯塔·Light-Tower', probe_name: 'docker', app_name: 'qbittorrent', app_type: 'docker', version: 'superng6/qbittorrentee', status: 'running', detail: { image: 'superng6/qbittorrentee', ports: [{ ip: '0.0.0.0', public: 8088, private: 8080, type: 'tcp' }] }, detected_at: now() - 60 },
  { id: 3, node_id: 'n_light', node_name: '灯塔·Light-Tower', probe_name: 'docker', app_name: 'FRPC', app_type: 'docker', version: 'stilleshan/frpc', status: 'stopped', detail: { image: 'docker.xuanyuan.me/stilleshan/frpc' }, detected_at: now() - 60 },
  { id: 4, node_id: 'n_light', node_name: '灯塔·Light-Tower', probe_name: 'nginx', app_name: 'nginx', app_type: 'nginx', version: '1.24.0', status: 'running', detail: { conf_files: ['nginx.conf', 'conf.d'], conf_dir: '/etc/nginx', config_candidates: [{ path: '/etc/nginx/nginx.conf', source: 'nginx -T', main: true }, { path: '/etc/nginx/conf.d/default.conf', source: 'nginx -T' }] }, detected_at: now() - 60 },
  { id: 5, node_id: 'n_aliyun', node_name: '阿里云·杭州/web-01', probe_name: 'docker', app_name: 'MoonShepherd', app_type: 'docker', version: 'ghcr.io/aealen/moonshepherd:latest', status: 'running', detail: { image: 'ghcr.io/aealen/moonshepherd:latest', ports: [{ ip: '0.0.0.0', public: 8080, private: 8080, type: 'tcp' }] }, detected_at: now() - 45 },
  { id: 6, node_id: 'n_aliyun', node_name: '阿里云·杭州/web-01', probe_name: 'docker', app_name: 'caddy', app_type: 'caddy', version: '2.8.4', status: 'running', detail: { config_candidates: [{ path: '/etc/caddy/Caddyfile', source: 'caddy adapt', main: true }] }, detected_at: now() - 45 },
  { id: 7, node_id: 'n_aliyun', node_name: '阿里云·杭州/web-01', probe_name: 'systemd', app_name: 'sshd', app_type: 'systemd', version: '', status: 'running', detail: { description: 'OpenBSD Secure Shell server' }, detected_at: now() - 45 },
  { id: 8, node_id: 'n_aliyun', node_name: '阿里云·杭州/web-01', probe_name: 'systemd', app_name: 'caddy', app_type: 'systemd', version: '', status: 'running', detail: { description: 'Caddy web server' }, detected_at: now() - 45 },
]

// 指令:状态全谱
const cmdTypes = ['exec', 'docker.op', 'probe', 'config.apply']
const commands = Array.from({ length: 14 }, (_, i) => {
  const n = nodes[i % 2]
  const type = cmdTypes[i % 4]
  const statuses = ['success', 'success', 'success', 'failed', 'running', 'pending', 'lost']
  const status = statuses[i % 7]
  return {
    command_id: `c_mock${String(i).padStart(4, '0')}`,
    node_id: n.id, node_name: n.name,
    type, status,
    payload: type === 'exec' ? { cmd: ['df -h /', 'docker ps --format "{{.Names}}"', 'uptime && free -m', 'tail -n 50 /var/log/syslog'][i % 4] }
      : type === 'docker.op' ? { op: ['restart', 'logs', 'pull', 'stop'][i % 4], container: apps[i % 3].app_name, tail_lines: 200 }
      : type === 'probe' ? { probe_names: [] }
      : { app_type: 'nginx', target_path: '/etc/nginx/conf.d/mock.conf' },
    timeout_s: 120,
    created_at: now() - i * 620,
    result: status === 'success' ? { status: 'success', exit_code: 0, stdout: `total 4.0K\ndrwxr-xr-x 1 root root 4096 Oct 26 23:00 .\n(模拟输出 ${i})` }
      : status === 'failed' ? { status: 'failed', exit_code: 1, stderr: 'exit status 1: no such container', error: '执行失败' }
      : undefined,
    created_by: 'admin',
  }
})

const auditLogs = Array.from({ length: 22 }, (_, i) => ({
  id: 100 - i, user_id: 1, username: 'admin',
  action: ['auth.login', 'command.create', 'session.open', 'session.input', 'config.apply', 'node.update'][i % 6],
  target: nodes[i % 2].id,
  detail: ['{}', '{"type":"docker.op"}', '{"session_id":"t_mock01"}', '{"cmd":"docker ps"}', '{"target":"/etc/caddy/Caddyfile"}', '{"name":"阿里云·杭州/web-01"}'][i % 6],
  ip: '10.20.40.14',
  created_at: now() - i * 900,
}))

// ---------- 简易路由 ----------

function json(data, status = 200, headers = {}) {
  return { status, headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(data) }
}
function page(data, p = 1, ps = 20, total = data.length) {
  return json({ data: data.slice((p - 1) * ps, p * ps), page: p, page_size: ps, total })
}

export function mockPlugin() {
  return {
    name: 'herd-mock-api',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = (req.url || '').split('?')[0]
        if (!url.startsWith('/api/v1/')) return next()
        const q = new URLSearchParams((req.url || '').split('?')[1] || '')
        const send = (r) => { res.statusCode = r.status; Object.entries(r.headers).forEach(([k, v]) => res.setHeader(k, v)); res.end(r.body) }

        // 免登录(开发走查)
        if (url === '/api/v1/auth/login' && req.method === 'POST') {
          return send(json({ username: 'admin', role: 'admin', expires_at: now() + 43200 }, 200, { 'Set-Cookie': 'herd_session=mock; Path=/' }))
        }
        if (url === '/api/v1/auth/me') return send(json({ username: 'admin', role: 'admin' }))

        if (url === '/api/v1/nodes' && req.method === 'GET') {
          const st = q.get('status')
          let list = nodes
          if (st) list = list.filter((n) => n.status === st)
          return send(page(list, Number(q.get('page') ?? 1), Number(q.get('page_size') ?? 20), list.length))
        }
        const mNode = url.match(/^\/api\/v1\/nodes\/(n_\w+)$/)
        if (mNode && req.method === 'GET') {
          const n = nodes.find((x) => x.id === mNode[1])
          if (!n) return send(json({ error: { code: 'not_found', message: '节点不存在' } }, 404))
          const latest = [...metrics].reverse().find((m) => m.node_id === n.id)
          return send(json({ node: n, latest_metric: latest ?? null }))
        }
        if (mNode && req.method === 'PATCH') {
          let body = ''
          req.on('data', (c) => (body += c))
          req.on('end', () => {
            const n = nodes.find((x) => x.id === mNode[1])
            if (n) { try { const p = JSON.parse(body); if (p.name) n.name = p.name; if (p.tags) n.tags = p.tags } catch { /* noop */ } }
            send(json({ ok: true }))
          })
          return
        }
        if (url === '/api/v1/nodes/registration-tokens' && req.method === 'POST') {
          return send(json({ id: 9, token: 'rt_mock' + randomUUID().replaceAll('-', '').slice(0, 40), expires_at: now() + 86400, note: q.get('note') ?? '' }, 201))
        }

        if (url === '/api/v1/apps') {
          const fNode = q.get('node_id')
          return send(page(fNode ? apps.filter((a) => a.node_id === fNode) : apps, Number(q.get('page') ?? 1), Number(q.get('page_size') ?? 20)))
        }
        const mApps = url.match(/^\/api\/v1\/nodes\/(n_\w+)\/apps$/)
        if (mApps) return send(json({ data: apps.filter((a) => a.node_id === mApps[1]) }))

        if (url === '/api/v1/commands') {
          const fNode = q.get('node_id')
          return send(page(fNode ? commands.filter((c) => c.node_id === fNode) : commands, Number(q.get('page') ?? 1), Number(q.get('page_size') ?? 20)))
        }
        const mCmdRetry = url.match(/^\/api\/v1\/commands\/(c_\w+)\/retry$/)
        if (mCmdRetry && req.method === 'POST') return send(json({ command_id: 'c_mock' + randomUUID().slice(0, 8) }))

        if (url === '/api/v1/audit-logs') return send(page(auditLogs, Number(q.get('page') ?? 1), Number(q.get('page_size') ?? 20)))
        if (url === '/api/v1/system/info') return send(json({ panel_version: 'main-d70fa47', go_version: 'go1.27', os: 'windows', arch: 'amd64', data_dir: './data-dev', node_count: nodes.length, online_count: 2, app_count: apps.length, uptime_s: now() - start }))
        // 文件只读查看:返回仿真配置内容(真实环境经 WSS 转推,此处直接同步给全文)
        const mFileRead = url.match(/^\/api\/v1\/nodes\/(n_\w+)\/file\/read$/)
        if (mFileRead && req.method === 'POST') {
          let body = ''
          req.on('data', (c) => (body += c))
          req.on('end', () => {
            const p = JSON.parse(body || '{}')
            const sample = p.path?.endsWith('.conf')
              ? 'user  nginx;\nworker_processes  auto;\n\nevents {\n    worker_connections  1024;\n}\n\nhttp {\n    include       /etc/nginx/mime.types;\n    default_type  application/octet-stream;\n\n    # gzip\n    gzip  on;\n    gzip_vary on;\n\n    server {\n        listen       80;\n        server_name  demo.aowu.tech;\n\n        location / {\n            proxy_pass         http://127.0.0.1:3000;\n            proxy_set_header   Host $host;\n            proxy_set_header   X-Real-IP $remote_addr;\n        }\n    }\n}\n'
              : '# Caddyfile\ndemo.aowu.tech {\n    reverse_proxy 127.0.0.1:3000\n    encode gzip\n}\n'
            send(json({
              request_id: 'fr_mock' + Date.now(),
              content: sample,
              size: sample.length,
              truncated: false,
            }))
          })
          return
        }
        // 日志流:开流返回 ID(演示模式:弹窗显示"等待日志…";真实环境走 WSS 推送)
        const mLogOpen = url.match(/^\/api\/v1\/nodes\/(n_\w+)\/logstream$/)
        if (mLogOpen && req.method === 'POST') {
          let body = ''
          req.on('data', (c) => (body += c))
          req.on('end', () => send(json({ stream_id: 'ls_mock' + Date.now() })))
          return
        }
        const mLogStop = url.match(/^\/api\/v1\/nodes\/n_\w+\/logstream\/(ls_\w+)$/)
        if (mLogStop && req.method === 'DELETE') return send(json({ status: 'stopped' }))
        if (url === '/api/v1/events') return send(json({})) // WS 升级请求:开发走查不提供实时推送
        const mNodeConfigs = url.match(/^\/api\/v1\/nodes\/(n_\w+)\/configs$/)
        if (mNodeConfigs) {
          return send(page([
            { id: 1, node_id: mNodeConfigs[1], app_type: 'nginx', target_path: '/etc/nginx/nginx.conf', latest_version: 3, status: 'in_sync', updated_at: now() - 3600 },
          ]))
        }
        return next() // 其余路径放行(404 由后端语义兜底)
      })
    },
  }
}
