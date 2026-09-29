<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAuthStore } from '@/stores/auth'
import { useSubjectStore } from '@/stores/subject'
import QuestionBasketButton from '@/components/QuestionBasketButton.vue'
import type { Permission } from '@/api/types'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const subject = useSubjectStore()

const collapsed = ref(false)

interface MenuItem {
  name: string
  title: string
  icon: string
  /** 需要的权限（在当前学科作用域内）。为空表示所有登录用户可见。 */
  permission?: Permission
  /** 仅管理员可见。 */
  admin?: boolean
}

const menus: MenuItem[] = [
  { name: 'dashboard', title: '总览', icon: 'DataBoard' },
  { name: 'questions', title: '题库', icon: 'Collection', permission: 'view_question' },
  { name: 'papers', title: '试卷', icon: 'Document', permission: 'view_question' },
  { name: 'recycle-bin', title: '回收站', icon: 'Delete', permission: 'view_question' },
  { name: 'import', title: '智能录题', icon: 'MagicStick', permission: 'edit_question' },
  { name: 'import-history', title: '导入历史', icon: 'Clock', permission: 'edit_question' },
  { name: 'chat', title: 'AI 助手', icon: 'ChatDotRound', permission: 'view_question' },
  { name: 'knowledge-points', title: '知识点', icon: 'Share', permission: 'view_question' },
  { name: 'tags', title: '标签', icon: 'PriceTag', permission: 'view_question' },
  { name: 'subjects', title: '学科管理', icon: 'School', admin: true },
  { name: 'users', title: '用户管理', icon: 'User', admin: true },
  { name: 'activity-logs', title: '操作日志', icon: 'Finished', admin: true },
  { name: 'ai-settings', title: 'AI 设置', icon: 'Setting', admin: true },
  { name: 'prompts', title: '提示词', icon: 'EditPen', admin: true },
  { name: 'settings', title: '系统设置', icon: 'Tools', admin: true },
]

const visibleMenus = computed(() =>
  menus.filter((item) => {
    if (item.admin) return auth.isSuperuser
    if (item.permission) return subject.can(item.permission)
    return true
  }),
)

/** 子页面（如组卷工作台、版本预览）不在菜单里，但要让父菜单保持高亮。 */
const menuAliases: Record<string, string> = {
  'paper-editor': 'papers',
  'paper-version': 'papers',
}

const activeMenu = computed(() => {
  const current = (route.name as string) ?? 'dashboard'
  return menuAliases[current] ?? current
})

onMounted(async () => {
  // 布局挂载时加载学科列表：几乎所有页面都依赖"当前学科"。
  if (!subject.hasSubjects) {
    try {
      await subject.load()
    } catch {
      // 错误提示已由接口层统一弹出，这里只需保证页面还能渲染。
    }
  }
})

async function onSwitchSubject(id: number) {
  await subject.switchTo(id)
  // 切学科后刷新当前页面的数据。用 replace 避免在历史里堆一串同学科记录。
  await router.replace({ path: route.path, query: { ...route.query, _s: String(id) } })
}

async function onLogout() {
  try {
    await ElMessageBox.confirm('确定要退出登录吗？', '退出登录', {
      confirmButtonText: '退出',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    return // 用户取消
  }
  await auth.logout()
  ElMessage.success('已退出登录')
  void router.replace({ name: 'login' })
}
</script>

<template>
  <el-container class="h-full">
    <el-aside :width="collapsed ? '64px' : '210px'" class="border-r border-gray-200 transition-all">
      <div class="flex h-14 items-center gap-2 px-4">
        <div
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-blue-600 text-sm font-bold text-white"
        >
          QM
        </div>
        <span v-show="!collapsed" class="truncate text-base font-semibold">QuestionMind</span>
      </div>

      <el-menu :default-active="activeMenu" :collapse="collapsed" class="border-r-0">
        <el-menu-item
          v-for="item in visibleMenus"
          :key="item.name"
          :index="item.name"
          @click="router.push({ name: item.name })"
        >
          <el-icon><component :is="item.icon" /></el-icon>
          <template #title>{{ item.title }}</template>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="flex items-center justify-between border-b border-gray-200 px-4">
        <div class="flex items-center gap-3">
          <el-button text :icon="collapsed ? 'Expand' : 'Fold'" @click="collapsed = !collapsed" />
          <el-breadcrumb separator="/">
            <el-breadcrumb-item>{{ (route.meta.title as string) ?? '' }}</el-breadcrumb-item>
          </el-breadcrumb>
        </div>

        <div class="flex items-center gap-3">
          <el-select
            v-if="subject.hasSubjects"
            :model-value="subject.currentId ?? undefined"
            placeholder="选择学科"
            class="w-40"
            size="default"
            @change="onSwitchSubject"
          >
            <el-option
              v-for="item in subject.subjects"
              :key="item.id"
              :label="item.name"
              :value="item.id"
            >
              <span>{{ item.name }}</span>
              <span class="ml-2 text-xs text-gray-400">{{ item.role_label }}</span>
            </el-option>
          </el-select>
          <el-text v-else type="info" size="small">尚无学科</el-text>

          <el-dropdown>
            <span class="flex cursor-pointer items-center gap-2">
              <el-avatar :size="28">{{ auth.displayName.slice(0, 1) || 'U' }}</el-avatar>
              <span class="text-sm">{{ auth.displayName }}</span>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item disabled>
                  {{ auth.isSuperuser ? '全局管理员' : '普通用户' }}
                </el-dropdown-item>
                <el-dropdown-item divided @click="router.push({ name: 'profile' })">
                  个人资料
                </el-dropdown-item>
                <el-dropdown-item @click="onLogout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <el-main class="bg-gray-50">
        <RouterView v-if="!subject.loading" />
        <div v-else class="flex h-full items-center justify-center">
          <el-icon class="is-loading" :size="28"><Loading /></el-icon>
        </div>
      </el-main>
    </el-container>

    <!--
      试题篮挂在布局上而不是各个列表页：篮子要能跨页面攒题，
      老师在题库选中、在试卷页才倒进去。篮子为空时它自己不渲染。
    -->
    <QuestionBasketButton />
  </el-container>
</template>

<style scoped>
:deep(.el-aside) {
  background-color: var(--el-bg-color);
}
:deep(.el-header) {
  background-color: var(--el-bg-color);
  height: 56px;
}
</style>
