<script setup lang="ts">
import { computed } from 'vue'
import { escapeHtml, renderMath } from '@/lib/math'

/**
 * 富文本渲染器。
 *
 * 为什么自己写而不是复用编辑器：编辑器是**可编辑**的，会为每个节点
 * 挂上 ProseMirror 的交互层。在列表、预览、快照这类只读场景里，
 * 一页几十个编辑器实例会明显卡顿。只读渲染用不到那些能力，
 * 直接产出 HTML 又快又轻。
 *
 * 安全性：所有文本都经 escapeHtml，公式交给 KaTeX。
 * 不使用 v-html 渲染用户原文，只渲染这里生成的、已转义的 HTML。
 */
const props = withDefaults(
  defineProps<{
    doc: unknown
    /** 紧凑模式：减少段落间距，用于列表里的题干预览。 */
    compact?: boolean
  }>(),
  { compact: false },
)

interface Node {
  type?: string
  text?: string
  marks?: { type: string; attrs?: Record<string, unknown> }[]
  attrs?: Record<string, unknown>
  content?: Node[]
}

const nodes = computed<Node[]>(() => {
  const doc = props.doc as Node | null | undefined
  if (!doc || typeof doc !== 'object') return []
  // 兼容 {type:'doc', content:[...]} 与直接给数组两种形态。
  if (Array.isArray(doc)) return doc as Node[]
  return Array.isArray(doc.content) ? doc.content : []
})

/** 把整棵文档树渲染为 HTML 字符串。 */
const html = computed(() => renderNodes(nodes.value))

function renderNodes(list: Node[]): string {
  return list.map(renderNode).join('')
}

function renderNode(node: Node): string {
  if (!node || typeof node !== 'object') return ''

  switch (node.type) {
    case 'text':
      return applyMarks(escapeHtml(node.text ?? ''), node.marks)

    case 'paragraph':
      return `<p class="${props.compact ? 'my-0.5' : 'my-1.5'} leading-relaxed">${renderNodes(
        node.content ?? [],
      )}</p>`

    case 'heading': {
      const level = clampLevel(node.attrs?.level)
      const size = ['text-lg', 'text-base', 'text-sm'][level - 1] ?? 'text-base'
      return `<p class="font-semibold ${size} my-2">${renderNodes(node.content ?? [])}</p>`
    }

    case 'inlineMath':
      return renderMath(String(node.attrs?.latex ?? ''))

    case 'blockMath':
      return `<span class="block my-2 text-center">${renderMath(
        String(node.attrs?.latex ?? ''),
        true,
      )}</span>`

    case 'hardBreak':
      return '<br />'

    case 'blank':
      // 空位在只读视图里显示为一段下划线，与纸面一致。
      return '<span class="inline-block align-bottom" style="min-width:4rem;border-bottom:1px solid currentColor">&nbsp;</span>'

    case 'image': {
      const src = String(node.attrs?.src ?? '')
      if (!src) return ''
      const width = node.attrs?.width ? `width:${Number(node.attrs.width)}px;` : ''
      return `<img src="${escapeHtml(src)}" style="${width}max-width:100%;border-radius:4px;margin:4px 0" alt="" />`
    }

    case 'bulletList':
    case 'orderedList': {
      const tag = node.type === 'bulletList' ? 'ul' : 'ol'
      const cls = tag === 'ul' ? 'list-disc' : 'list-decimal'
      return `<${tag} class="${cls} pl-6 my-1.5">${renderNodes(node.content ?? [])}</${tag}>`
    }

    case 'listItem':
      return `<li>${renderNodes(node.content ?? [])}</li>`

    case 'table': {
      const rows = (node.content ?? []).map(renderNode).join('')
      return `<table class="my-2 w-full border-collapse text-sm">${rows}</table>`
    }

    case 'tableRow':
      return `<tr>${renderNodes(node.content ?? [])}</tr>`

    case 'tableHeader':
    case 'tableCell': {
      const tag = node.type === 'tableHeader' ? 'th' : 'td'
      const extra = tag === 'th' ? 'font-semibold bg-gray-50' : ''
      return `<${tag} class="border border-gray-300 px-2 py-1 align-top ${extra}">${renderNodes(
        node.content ?? [],
      )}</${tag}>`
    }

    case 'codeBlock':
      return `<pre class="my-2 overflow-x-auto rounded bg-gray-100 p-2 text-xs"><code>${escapeHtml(
        plainText(node),
      )}</code></pre>`

    default:
      // 未知节点：尽力把子节点渲染出来，别丢内容。
      return node.content ? renderNodes(node.content) : ''
  }
}

function applyMarks(text: string, marks?: Node['marks']): string {
  if (!marks?.length) return text
  let out = text
  for (const mark of marks) {
    switch (mark.type) {
      case 'bold':
      case 'strong':
        out = `<strong>${out}</strong>`
        break
      case 'italic':
      case 'em':
        out = `<em>${out}</em>`
        break
      case 'underline':
        out = `<u>${out}</u>`
        break
      case 'strike':
        out = `<s>${out}</s>`
        break
      case 'code':
        out = `<code class="rounded bg-gray-100 px-1 py-0.5 text-xs">${out}</code>`
        break
      case 'superscript':
        out = `<sup>${out}</sup>`
        break
      case 'subscript':
        out = `<sub>${out}</sub>`
        break
      case 'link': {
        const href = String(mark.attrs?.href ?? '')
        // 只允许 http(s)：javascript: 之类的伪协议在 v-html 里是 XSS 入口。
        if (/^https?:\/\//i.test(href)) {
          out = `<a href="${escapeHtml(href)}" target="_blank" rel="noopener noreferrer" class="text-blue-600 underline">${out}</a>`
        }
        break
      }
      default:
        break
    }
  }
  return out
}

function clampLevel(raw: unknown): number {
  const n = Number(raw)
  if (!Number.isFinite(n)) return 1
  return Math.min(Math.max(Math.round(n), 1), 3)
}

function plainText(node: Node): string {
  let out = ''
  if (node.text) out += node.text
  for (const child of node.content ?? []) out += plainText(child)
  return out
}
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -- 内容已在上方逐节点转义 -->
  <div class="rich-content" v-html="html" />
</template>

<style scoped>
.rich-content :deep(.katex) {
  font-size: 1.05em;
}
.rich-content :deep(p:first-child) {
  margin-top: 0;
}
.rich-content :deep(p:last-child) {
  margin-bottom: 0;
}
</style>
