<script setup lang="ts">
import { computed } from 'vue'
import MarkdownIt from 'markdown-it'
import texmath from 'markdown-it-texmath'
import katex from 'katex'

const props = defineProps<{ content: string }>()

const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
})
md.use(texmath, {
  engine: katex,
  delimiters: 'dollars',
  katexOptions: { throwOnError: false },
})

const html = computed(() => md.render(props.content || ''))
</script>

<template>
  <div class="qm-markdown text-sm" v-html="html" />
</template>

<style scoped>
.qm-markdown :deep(p) {
  margin: 0.35em 0;
}
.qm-markdown :deep(p:first-child) {
  margin-top: 0;
}
.qm-markdown :deep(p:last-child) {
  margin-bottom: 0;
}
.qm-markdown :deep(ul),
.qm-markdown :deep(ol) {
  margin: 0.35em 0;
  padding-left: 1.4em;
}
.qm-markdown :deep(ul) {
  list-style: disc;
}
.qm-markdown :deep(ol) {
  list-style: decimal;
}
.qm-markdown :deep(code) {
  border-radius: 0.25rem;
  background: color-mix(in srgb, currentColor 10%, transparent);
  padding: 0.1em 0.3em;
  font-size: 0.9em;
}
.qm-markdown :deep(pre) {
  margin: 0.5em 0;
  overflow-x: auto;
  border-radius: 0.375rem;
  background: #1e293b;
  padding: 0.65rem 0.75rem;
  color: #e2e8f0;
}
.qm-markdown :deep(pre code) {
  background: transparent;
  padding: 0;
}
.qm-markdown :deep(a) {
  color: var(--qm-accent-text);
  text-decoration: underline;
  text-underline-offset: 2px;
}
.qm-markdown :deep(table) {
  display: block;
  max-width: 100%;
  overflow-x: auto;
  border-collapse: collapse;
  margin: 0.5em 0;
}
.qm-markdown :deep(th),
.qm-markdown :deep(td) {
  border: 1px solid var(--qm-hairline);
  padding: 0.25rem 0.45rem;
}
.qm-markdown :deep(.katex-display) {
  margin: 0.5em 0;
  overflow-x: auto;
  overflow-y: hidden;
}
</style>
