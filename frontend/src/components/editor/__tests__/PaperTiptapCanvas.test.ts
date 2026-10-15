// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import PaperTiptapCanvas from '../PaperTiptapCanvas.vue'
import type { PaperNode, PaperQuestionProjection } from '@/api/paper'
import { plainToRichDoc } from '@/lib/richdoc'

function questionNode(): PaperNode {
  return {
    id: 'q1',
    parent_id: null,
    position: 0,
    node_kind: 'block',
    node_type: 'question',
    content: { question_id: 1, content_revision: 1 },
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  }
}

function projection(): PaperQuestionProjection {
  return {
    id: 1,
    question_type: 'essay',
    question_type_label: '解答题',
    difficulty: 3,
    stem: plainToRichDoc('渲染回归题'),
    options: [],
    answer: { kind: 'reference' },
    analysis: null,
    solution: null,
    summary: null,
    stem_text: '渲染回归题',
    content_revision: 1,
    stale: false,
  }
}

let mounted: ReturnType<typeof createApp> | null = null
let host: HTMLElement | null = null

afterEach(() => {
  mounted?.unmount()
  host?.remove()
  mounted = null
  host = null
  vi.restoreAllMocks()
})

describe('PaperTiptapCanvas', () => {
  it('挂载时不触发未初始化变量错误', async () => {
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    host = document.createElement('div')
    document.body.appendChild(host)
    mounted = createApp(PaperTiptapCanvas, {
      nodes: [questionNode()],
      projections: { 1: projection() },
      canEdit: true,
      numberingEnabled: true,
      scoringEnabled: false,
    })
    mounted.mount(host)
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(host.textContent).toContain('渲染回归题')
  })
})
