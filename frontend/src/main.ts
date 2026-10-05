import { createApp } from 'vue'
import { createPinia } from 'pinia'

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

const app = createApp(App)

// 主题要在首屏渲染前落到 <html> 上，否则会闪一下浅色。
applyInitialTheme()

// Element Plus 与图标都不再全量注册：组件与图标由构建插件按需引入，
// 中文语言包由根组件的 <el-config-provider> 提供（见 App.vue）。
app.use(createPinia())
app.use(router)

app.mount('#app')
