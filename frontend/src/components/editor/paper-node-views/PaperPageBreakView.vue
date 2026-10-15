<script setup lang="ts">
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'

/**
 * 分页符的 NodeView。
 *
 * 纯视觉分隔，没有可编辑内容，也没有属性入口——它唯一的属性是 nodeId。
 * 整块 contenteditable=false：不这样的话光标会停进这个原子节点里，
 * 出现"删一下多出一个分页符"的错觉。
 */
const props = defineProps(nodeViewProps)
</script>

<template>
  <NodeViewWrapper
    class="qm-canvas-pagebreak"
    :class="selected ? 'qm-canvas-block--selected' : ''"
    data-qm-node="page_break"
    :data-node-id="props.node.attrs.nodeId"
    data-paper-block
    contenteditable="false"
  >
    <div class="qm-canvas-pagebreak__rule" />
    <span class="qm-canvas-pagebreak__label">分页符</span>
    <div class="qm-canvas-pagebreak__rule" />
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-pagebreak {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.75rem 0.75rem;
  color: var(--el-text-color-placeholder);
  font-size: 0.75rem;
  user-select: none;
}
.qm-canvas-pagebreak__rule {
  flex: 1;
  border-top: 1px dashed var(--el-border-color);
}
.qm-canvas-pagebreak__label {
  white-space: nowrap;
}
</style>
