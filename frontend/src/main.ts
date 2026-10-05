import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { ElMessage } from 'element-plus'

// ElMessage / ElMessageBox 这类**程序化** API 不在模板里出现，
// 组件解析器（unplugin-vue-components）管不到它们，样式必须在入口显式引入。
// 将来再用别的程序化 API（ElNotification 等），记得补一行对应的 style/css。
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'

import 'katex/dist/katex.min.css'
import './styles/main.css'

import App from './App.vue'
import { router } from './router'
import { applyInitialTheme } from './stores/theme'

/**
 * 全局错误兜底。
 *
 * 渲染崩溃由 App.vue 里的 `<ErrorBoundary>` 截住了（它返回 false，不会走到这里），
 * 所以这里负责的是剩下那半边：事件回调、watch、定时器、第三方库抛的错。
 * 它们以前只会变成控制台里一行没人看的红字，用户看到的是"点了没反应"。
 *
 * 未处理的 Promise 拒绝只记日志不弹提示：路由跳转的中止与重复也走那条路，
 * 弹出去会把正常操作说成出错。
 */
function report(source: string, error: unknown, notify: boolean) {
  console.error(`[${source}]`, error)
  if (!notify) return
  const message = error instanceof Error ? error.message : String(error)
  ElMessage.error(`操作失败：${message}`)
}

const app = createApp(App)

// 主题要在首屏渲染前落到 <html> 上，否则会闪一下浅色。
applyInitialTheme()

app.config.errorHandler = (error, _instance, info) => report(`vue:${info}`, error, true)

window.addEventListener('error', (event) => {
  // 资源加载失败（图片、脚本）也走这个事件，但没有 error 对象：
  // 那种情况弹提示只会变成噪声，记一行日志就够了。
  if (event.error) {
    report('window', event.error, true)
    return
  }
  console.error('[window]', event.message)
})

window.addEventListener('unhandledrejection', (event) => {
  report('unhandledrejection', event.reason, false)
})

// Element Plus 与图标都不再全量注册：组件与图标由构建插件按需引入，
// 中文语言包由根组件的 <el-config-provider> 提供（见 App.vue）。
app.use(createPinia())
app.use(router)

app.mount('#app')
