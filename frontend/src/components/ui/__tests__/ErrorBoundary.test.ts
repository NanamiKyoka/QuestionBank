// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import ErrorBoundary from '../ErrorBoundary.vue'

/**
 * 出错开关：打开时子组件在**渲染**里抛错，正是白屏的典型来源
 * （KaTeX 解析炸掉、组件拿到意料之外的数据）。
 */
const boom = ref(true)
const ticks = ref(0)

const Child = defineComponent({
  setup() {
    return () => {
      if (boom.value) throw new Error('渲染崩了')
      return h('p', `正常内容 ${ticks.value}`)
    }
  },
})

let app: App | null = null

function mount(): HTMLElement {
  const host = document.createElement('div')
  document.body.appendChild(host)
  app = createApp(
    defineComponent({
      render: () => h(ErrorBoundary, null, { default: () => h(Child) }),
    }),
  )
  app.mount(host)
  return host
}

function button(host: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(host.querySelectorAll('button')).find((btn) => btn.textContent?.includes(label))
}

beforeEach(() => {
  boom.value = true
  ticks.value = 0
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('ErrorBoundary', () => {
  it('子树渲染抛错时换成可操作的面板，而不是白屏', async () => {
    const host = mount()
    await nextTick()

    expect(host.textContent).toContain('这个页面渲染出错了')
    expect(host.textContent).toContain('渲染崩了')
    // 两个出口都得在：用户要能不刷新页面地再试一次，也要能彻底重来。
    expect(button(host, '重试')).toBeTruthy()
    expect(button(host, '重新加载页面')).toBeTruthy()
  })

  it('错误会被留下来，而不是悄悄吞掉', async () => {
    mount()
    await nextTick()

    expect(console.error).toHaveBeenCalledWith('[ErrorBoundary]', expect.any(Error))
  })

  it('重试换掉出错的子树，内容回来且页面没有刷新', async () => {
    const host = mount()
    await nextTick()
    boom.value = false

    button(host, '重试')?.click()
    await nextTick()

    expect(host.textContent).toContain('正常内容')
    expect(host.textContent).not.toContain('这个页面渲染出错了')
  })

  it('恢复之后再次崩溃仍会被截住', async () => {
    const host = mount()
    await nextTick()

    boom.value = false
    button(host, '重试')?.click()
    await nextTick()

    // 换个状态再让子组件渲染一次：这次它照样抛错。
    boom.value = true
    ticks.value += 1
    await nextTick()

    expect(host.textContent).toContain('这个页面渲染出错了')
  })
})
