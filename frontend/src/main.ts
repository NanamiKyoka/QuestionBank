import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

import 'element-plus/dist/index.css'
import 'katex/dist/katex.min.css'
import './styles/main.css'

import App from './App.vue'
import { router } from './router'

const app = createApp(App)

// 图标按需全局注册：Element Plus 的图标组件很多，
// 逐个 import 到各页面反而更啰嗦，统一注册更省事。
for (const [name, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(name, component)
}

app.use(createPinia())
app.use(router)
app.use(ElementPlus, { locale: zhCn })

app.mount('#app')
