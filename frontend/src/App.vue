<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { setAuthFailureHandlers } from '@/api/http'
import { markSetupComplete } from '@/router'
import { useAuthStore } from '@/stores/auth'

/**
 * 应用根组件。
 *
 * 这里做一件事：把"接口层发现的问题"翻译成"路由动作"。
 * 接口层不能直接 import router（router 依赖 store，store 依赖 api，
 * 会形成循环），所以由根组件在挂载时注册回调。
 */
const router = useRouter()
const auth = useAuthStore()

setAuthFailureHandlers({
  onUnauthorized: () => {
    // 凭证失效：清掉本地状态并回登录页，把当前地址带上以便登录后跳回。
    auth.setToken(null)
    auth.user = null
    auth.permissions = null
    const current = router.currentRoute.value
    if (current.name !== 'login') {
      void router.replace({ name: 'login', query: { redirect: current.fullPath } })
    }
  },
  onSetupRequired: () => {
    markSetupComplete()
    void router.replace({ name: 'setup' })
  },
})

onMounted(() => {
  // 触发一次初始化状态探测，避免用户直接访问深层地址时卡在空白页。
  void router.isReady()
})
</script>

<template>
  <RouterView />
</template>
