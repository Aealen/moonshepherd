import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('./views/Login.vue'), meta: { public: true } },
    { path: '/', redirect: '/nodes' },
    { path: '/nodes', component: () => import('./views/Nodes.vue') },
    { path: '/nodes/:id', component: () => import('./views/NodeDetail.vue') },
    { path: '/apps', component: () => import('./views/Apps.vue') },
    { path: '/commands', component: () => import('./views/Commands.vue') },
    { path: '/nodes/:id/configs/:targetId', component: () => import('./views/ConfigVersion.vue') },
    { path: '/system', component: () => import('./views/System.vue') },
    { path: '/audit', component: () => import('./views/Audit.vue') },
  ],
})

// 会话失效统一跳登录
router.beforeEach((to) => {
  if (to.meta.public) return true
  if (!localStorage.getItem('herd_auth_ok')) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  return true
})
