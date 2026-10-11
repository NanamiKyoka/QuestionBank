// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h, type App } from 'vue'
import MarkdownContent from '../MarkdownContent.vue'

const apps: App[] = []

function render(content: string): HTMLElement {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp({ render: () => h(MarkdownContent, { content }) })
  apps.push(app)
  app.mount(host)
  return host
}

afterEach(() => {
  for (const app of apps) app.unmount()
  apps.length = 0
  document.body.innerHTML = ''
})

describe('MarkdownContent', () => {
  it('渲染常用 Markdown 且不执行裸 HTML', () => {
    const host = render('**重点**\n\n<script>alert(1)</script>')
    expect(host.querySelector('strong')?.textContent).toBe('重点')
    expect(host.querySelector('script')).toBeNull()
  })

  it('渲染行内公式', () => {
    const host = render('函数 $f(x)=x^2$ 的图像')
    expect(host.querySelector('.katex')).not.toBeNull()
  })
})
