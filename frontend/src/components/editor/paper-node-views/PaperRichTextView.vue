<script setup lang="ts">
import { NodeViewContent, NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'

/**
 * 说明文字的 NodeView。
 *
 * 内容由 `NodeViewContent` 交给编辑器渲染——**不要**在这里自己 v-html，
 * 那会让这块内容脱离 ProseMirror 的文档，光标进去就乱。
 * 这里只负责外壳：一块可点可选的排版单元。
 */
const props = defineProps(nodeViewProps)
</script>

<template>
  <NodeViewWrapper
    class="qm-canvas-block qm-canvas-richtext"
    :class="selected ? 'qm-canvas-block--selected' : ''"
    data-qm-node="rich_text"
    :data-node-id="props.node.attrs.nodeId"
    data-paper-block
  >
    <NodeViewContent class="qm-canvas-richtext__body" />
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-richtext {
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  border: 1px solid transparent;
}
.qm-canvas-richtext:hover {
  border-color: var(--el-border-color-lighter);
}
.qm-canvas-richtext__body {
  outline: none;
  line-height: 1.7;
}
</style>
