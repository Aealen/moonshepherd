// 活跃终端会话全局表:侧边栏「活跃会话」列表的数据源。
// NodeTerminal 开/关会话时同步;节点详情页 KeepAlive 保活期间状态持续有效,
// 用户切到任意页面都能看到会话并一键跳回终端。
import { defineStore } from 'pinia'

export type TermSession = {
  nodeID: string
  nodeName: string
  sessionID: string
  openedAt: number // unix ms,侧边栏计时长用
}

export const useTerminalStore = defineStore('terminals', {
  state: () => ({
    sessions: [] as TermSession[],
    // 跨页跳转意图:侧边栏点会话 → NodeDetail 挂载/激活时读取并切到终端 tab。
    // 不走 URL query:router-view 按 fullPath 做 key,query 变化会重建实例,
    // KeepAlive 缓存失效反而断掉会话保活。
    pendingTabNodeID: '',
  }),
  actions: {
    open(nodeID: string, nodeName: string, sessionID: string) {
      this.sessions = this.sessions.filter((s) => s.nodeID !== nodeID) // 同节点仅保留最新会话
      this.sessions.push({ nodeID, nodeName, sessionID, openedAt: Date.now() })
    },
    close(sessionID: string) {
      this.sessions = this.sessions.filter((s) => s.sessionID !== sessionID)
    },
    requestTerminal(nodeID: string) {
      this.pendingTabNodeID = nodeID
    },
    takePendingTab(nodeID: string): boolean {
      if (this.pendingTabNodeID === nodeID) {
        this.pendingTabNodeID = ''
        return true
      }
      return false
    },
  },
})
