<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import ErrorBoundary from '@/components/ui/ErrorBoundary.vue'
import { setAuthFailureHandlers } from '@/api/http'
import { markSetupComplete } from '@/router'
import { useAuthStore } from '@/stores/auth'

/**
 * 应用根组件。
 *
 * 这里做三件事：
 *
 * 1. 用 `<el-config-provider>` 把 Element Plus 切到中文。按需引入之后
 *    `app.use(ElementPlus, { locale })` 那条路没了，而 provider 会同时把配置
 *    写进 Element Plus 的全局配置，连 ElMessageBox 这类脱离组件树的弹层也能读到。
 * 2. 把"接口层发现的问题"翻译成"路由动作"。接口层不能直接 import router
 *    （router 依赖 store，store 依赖 api，会形成循环），所以由根组件在挂载时注册回调。
 * 3. 把正文包进 <ErrorBoundary>：渲染崩溃时换掉的只是正文，侧边栏与顶栏照常可用，
 *    不至于整页白屏只能硬刷新（而刷新一次，正在编辑的东西就全没了）。
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
  <el-config-provider :locale="zhCn">
    <!-- h-full 不能去掉：布局根节点的 h-full 参照的是这个 div，
         它没有定高就会解析成 auto，el-main 的 overflow:auto 随之失效，
         滚的变成整个页面（侧边栏与顶栏会一起被滚走）。 -->
    <div class="flex h-full flex-col">
      <a href="#main-content" class="skip-link">跳到主要内容</a>
      <ErrorBoundary>
        <RouterView />
      </ErrorBoundary>
    </div>
  </el-config-provider>
</template>

<style scoped>
.skip-link {
  position: absolute;
  top: -100px;
  left: 8px;
  z-index: 2000;
  padding: 8px 14px;
  border-radius: 6px;
  background: var(--qm-accent);
  color: #fff;
  font-size: 13px;
  transition: top 0.15s ease;
}

.skip-link:focus {
  top: 8px;
}
</style>