<script setup lang="ts">
import { computed, onErrorCaptured, ref } from 'vue'

/**
 * 渲染错误边界。
 *
 * Vue 默认没有错误边界：一次渲染异常（KaTeX 解析炸了、某个组件拿到意料之外的数据、
 * 第三方库抛错）会让整棵树停在半路，用户看到的是白屏加一次硬刷新——
 * 而这个产品的用户正在编辑题干和卷子，刷新一次就全没了。
 *
 * 这里把崩溃挡在一层里：侧边栏和顶栏照常可用，正文换成一块可以操作的面板。
 * 两个按钮的代价不一样，写在面板上，别让用户替我们猜。
 */
const error = ref<unknown>(null)

onErrorCaptured((err) => {
  error.value = err
  // 返回 false 会就地截断错误传播：根组件的 errorHandler 不会再弹一次。
  // 截断的前提是这里自己把日志留下，否则这行错误就彻底没人知道了。
  console.error('[ErrorBoundary]', err)
  return false
})

const message = computed(() => {
  const err = error.value
  if (err instanceof Error) return err.message || String(err)
  return String(err)
})

/** 重试：把出错的那棵子树整个换掉重来，页面不刷新，内存里的编辑状态还在。 */
function retry() {
  error.value = null
}

function reload() {
  window.location.reload()
}
</script>

<template>
  <div
    v-if="error"
    role="alert"
    class="flex h-full flex-col items-center justify-center gap-3 px-6 text-center"
  >
    <el-icon :size="32" class="text-ink-subtle"><WarningFilled /></el-icon>
    <p class="text-sm font-medium text-ink-regular">这个页面渲染出错了</p>
    <p class="max-w-[60ch] break-all text-xs leading-relaxed text-ink-subtle">{{ message }}</p>

    <div class="flex gap-2">
      <el-button type="primary" @click="retry">重试</el-button>
      <el-button @click="reload">重新加载页面</el-button>
    </div>

    <p class="max-w-[60ch] text-xs leading-relaxed text-ink-muted">
      「重试」不刷新页面，编辑器里没保存的内容还在内存里；「重新加载」会丢掉它们。
      反复出错就把上面那行字发给管理员。
    </p>
  </div>

  <slot v-else />
</template>
