// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h, type App } from 'vue'
import RichContent from '../RichContent.vue'

const apps: App[] = []

/** 把文档渲染进真实 DOM，返回宿主节点。 */
function render(doc: unknown): HTMLElement {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp({ render: () => h(RichContent, { doc }) })
  apps.push(app)
  app.mount(host)
  return host
}

function imageDoc(attrs: Record<string, unknown>) {
  return { type: 'doc', content: [{ type: 'image', attrs }] }
}

afterEach(() => {
  for (const app of apps) app.unmount()
  apps.length = 0
  document.body.innerHTML = ''
})

describe('RichContent 的图片替代文字', () => {
  it('编辑器没存 alt 时也不留空——空 alt 会让读屏整张跳过图片', () => {
    const host = render(imageDoc({ src: '/uploads/2026/10/ab3f.png' }))

    expect(host.querySelector('img')?.getAttribute('alt')).toBe('题图')
  })

  it('存了 alt 就照搬，存的是 title 时退而用它', () => {
    const withAlt = render(imageDoc({ src: '/a.png', alt: '抛物线示意图' }))
    expect(withAlt.querySelector('img')?.getAttribute('alt')).toBe('抛物线示意图')

    const withTitle = render(imageDoc({ src: '/b.png', title: '统计图' }))
    expect(withTitle.querySelector('img')?.getAttribute('alt')).toBe('统计图')
  })

  it('alt 里的引号与尖括号被转义，不能借它截断标签', () => {
    const host = render(imageDoc({ src: '/a.png', alt: '"><script>alert(1)</script>' }))

    const img = host.querySelector('img')
    expect(img?.getAttribute('alt')).toBe('"><script>alert(1)</script>')
    expect(host.querySelectorAll('script')).toHaveLength(0)
  })
})
