import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import Components from 'unplugin-vue-components/vite'
import type { ComponentResolver } from 'unplugin-vue-components'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import * as ElementPlusIcons from '@element-plus/icons-vue'

// 后端地址：开发时通过 Vite 代理转发，避免 CORS。
const backend = process.env.QM_BACKEND ?? 'http://127.0.0.1:8080'

/**
 * 图标按需引入。
 *
 * 之前把 600 多个图标全量注册到全局（`app.component` 循环），模板里只用了二十来个，
 * 其余的照样进主 chunk。解析器只认模板里写死的名字，所以字符串形式的动态图标
 * （`<component :is="'Search'">`、`:icon="'Fold'"`）必须改成直接 import 组件。
 */
const iconResolver: ComponentResolver = {
  type: 'component',
  resolve(name) {
    if (name.startsWith('El')) return // El* 归 ElementPlusResolver
    if (Object.prototype.hasOwnProperty.call(ElementPlusIcons, name)) {
      return { name, from: '@element-plus/icons-vue' }
    }
  },
}

/**
 * 构建时剔除 KaTeX 的 woff / truetype 字体，只留 woff2。
 *
 * `katex.min.css` 为每款字体声明了三种 `format()`，Vite 于是把三种都拷进产物。
 * 现代浏览器一律走 woff2，剩下约 798 KB 是纯死重——还会被 go:embed 打进单文件二进制。
 *
 * 两步顺序不能反：先把 CSS 里多余的 `url()` 删掉，再删掉没人引用的字体文件；
 * 先删文件的话，CSS 会指着一堆 404。
 * 没有 woff2 兜底的字体（KaTeX_Size3）一个分支都不删，否则它在所有浏览器里都取不到字。
 */
function dropLegacyFonts(): Plugin {
  return {
    name: 'qm:drop-legacy-fonts',
    apply: 'build',
    generateBundle(_options, bundle) {
      const remainingCss = Object.keys(bundle)
        .filter((name) => name.endsWith('.css'))
        .map((name) => {
          const asset = bundle[name]
          if (asset.type !== 'asset' || typeof asset.source !== 'string') return ''
          const next = asset.source.replace(
            /src\s*:\s*([^;}]+)/g,
            (declaration: string, sources: string) => {
              if (!sources.includes('.woff2')) return declaration
              const parts = sources.split(',').map((part) => part.trim())
              // 只留 woff2 分支：`url(x.woff)` 会命中，`url(x.woff2)` 不会。
              const kept = parts.filter((part) => !/\.(woff|ttf)\)/.test(part))
              if (kept.length === parts.length) return declaration
              return `src:${kept.join(',')}`
            },
          )
          bundle[name] = { ...asset, source: next }
          return next
        })
        .join('')

      for (const name of Object.keys(bundle)) {
        if (!/\.(woff|ttf)$/.test(name)) continue
        if (remainingCss.includes(name.slice(name.lastIndexOf('/') + 1))) continue
        delete bundle[name]
      }
    },
  }
}

export default defineConfig({
  plugins: [
    vue(),
    tailwindcss(),
    // 按需引入：模板里用到哪个组件才打包哪个，连同它的样式。
    // 不这么做的话 `app.use(ElementPlus)` 会把 ElTree / ElTransfer / ElCalendar
    // 这些一个都没用上的组件塞进主 chunk（实测约 1.2 MB，含样式）。
    //
    // 测试环境只解析组件、不带样式：vitest 把 element-plus 当外部依赖交给 Node 加载，
    // 那条链上的 .css 会绕过 Vite，直接以 "Unknown file extension" 炸掉测试。
    Components({
      resolvers: [
        ElementPlusResolver({ importStyle: process.env.VITEST ? false : 'css' }),
        iconResolver,
      ],
      dts: false,
    }),
    dropLegacyFonts(),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // 与后端 main.go 的挂载点保持一致：/api、/static、/uploads。
      '/api': { target: backend, changeOrigin: true },
      '/static': { target: backend, changeOrigin: true },
      '/uploads': { target: backend, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    // 生产由后端或 nginx 托管静态文件，不需要 sourcemap。
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
})
