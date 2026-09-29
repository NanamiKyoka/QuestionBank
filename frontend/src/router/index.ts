import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { systemApi } from '@/api'
import { useAuthStore } from '@/stores/auth'

/**
 * 路由表。
 *
 * meta.requiresAuth 决定是否需要登录；meta.title 用于面包屑与侧边栏高亮。
 * 页面组件用动态 import：首屏只加载登录页，其余按需下载。
 */
const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { title: '登录', public: true },
  },
  {
    path: '/setup',
    name: 'setup',
    component: () => import('@/views/SetupView.vue'),
    meta: { title: '初始化', public: true },
  },
  {
    path: '/',
    component: () => import('@/layouts/DefaultLayout.vue'),
    children: [
      {
        path: '',
        name: 'dashboard',
        component: () => import('@/views/DashboardView.vue'),
        meta: { title: '总览' },
      },
      {
        path: 'questions',
        name: 'questions',
        component: () => import('@/views/QuestionsView.vue'),
        meta: { title: '题库' },
      },
      {
        path: 'knowledge-points',
        name: 'knowledge-points',
        component: () => import('@/views/KnowledgePointsView.vue'),
        meta: { title: '知识点' },
      },
      {
        path: 'papers',
        name: 'papers',
        component: () => import('@/views/PapersView.vue'),
        meta: { title: '试卷' },
      },
      {
        // 编辑台独占整页：组卷需要横向空间，塞进布局内容区会很挤。
        path: 'papers/:id/edit',
        name: 'paper-editor',
        component: () => import('@/views/PaperEditorView.vue'),
        meta: { title: '组卷工作台' },
      },
      {
        path: 'tags',
        name: 'tags',
        component: () => import('@/views/TagsView.vue'),
        meta: { title: '标签' },
      },
      {
        path: 'import',
        name: 'import',
        component: () => import('@/views/ImportView.vue'),
        meta: { title: '智能录题' },
      },
      {
        path: 'chat',
        name: 'chat',
        component: () => import('@/views/ChatView.vue'),
        meta: { title: 'AI 助手' },
      },
      {
        path: 'ai-settings',
        name: 'ai-settings',
        component: () => import('@/views/AISettingsView.vue'),
        meta: { title: 'AI 设置', admin: true },
      },
      {
        path: 'subjects',
        name: 'subjects',
        component: () => import('@/views/SubjectsView.vue'),
        meta: { title: '学科管理', admin: true },
      },
      {
        path: 'users',
        name: 'users',
        component: () => import('@/views/UsersView.vue'),
        meta: { title: '用户管理', admin: true },
      },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    redirect: '/',
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

// 初始化状态只需要探测一次：这是一个全局性的"系统装好了吗"。
let setupChecked = false
let needsSetup = false

export function markSetupComplete() {
  needsSetup = false
  setupChecked = true
}

router.beforeEach(async (to) => {
  const auth = useAuthStore()

  // 1) 第一次导航时探测初始化状态。未初始化的系统只有 /setup 可用。
  if (!setupChecked) {
    try {
      const status = await systemApi.setupStatus()
      needsSetup = status.needs_setup
    } catch {
      // 探测失败不拦路：后端可能刚好在重启。
      // 让请求继续走到接口层，由那里的错误提示告诉用户后端不可用。
      needsSetup = false
    }
    setupChecked = true
  }
  if (needsSetup && to.name !== 'setup') {
    return { name: 'setup' }
  }
  if (!needsSetup && to.name === 'setup') {
    // 已经初始化过了就别停留在安装向导。
    return { name: auth.isAuthenticated ? 'dashboard' : 'login' }
  }

  // 2) 恢复登录态。只做一次，后续靠 store 里的状态。
  if (!auth.loaded) {
    await auth.restore()
  }

  // 3) 公开页面直接放行。
  if (to.meta.public) {
    // 已登录还去登录页就直接回首页。
    if (to.name === 'login' && auth.isAuthenticated) {
      return { name: 'dashboard' }
    }
    return true
  }

  // 4) 需要登录。
  if (!auth.isAuthenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }

  // 5) 仅管理员页面。
  if (to.meta.admin && !auth.isSuperuser) {
    return { name: 'dashboard' }
  }

  return true
})

router.afterEach((to) => {
  const title = (to.meta.title as string | undefined) ?? ''
  document.title = title ? `${title} · QuestionMind` : 'QuestionMind · AI 原生题库'
})
