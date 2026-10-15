<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewContent, NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import { MODULE_SCOPES, type ModuleScopeValue } from '@/lib/paperDocument'
import PaperBlockActions from './PaperBlockActions.vue'
import { paperCanvasStateOf } from './canvasState'

/**
 * 解析模块的 NodeView。
 *
 * 它是画布上唯一带子节点的块：里面的解析条目由服务端派生，
 * 这里只管外壳（范围与标题）与内容容器。
 *
 * `contenteditable="false"` 只加在控制条上，不加在外层——
 * 外层一旦关掉可编辑，子节点里将来要放的可编辑内容就全废了。
 */
const props = defineProps(nodeViewProps)

const state = computed(() => paperCanvasStateOf(props.editor))

const scope = computed(() => {
  const raw = props.node.attrs.scope
  return MODULE_SCOPES.includes(raw as ModuleScopeValue) ? (raw as ModuleScopeValue) : 'all'
})

const title = computed(() => {
  const raw = props.node.attrs.title
  return typeof raw === 'string' ? raw : ''
})

function setScope(next: ModuleScopeValue) {
  if (!state.value.canEdit) return
  props.updateAttributes({ scope: next })
}

function setTitle(next: string) {
  if (!state.value.canEdit) return
  const trimmed = (next ?? '').slice(0, 128)
  props.updateAttributes({ title: trimmed || null })
}
</script>

<template>
  <NodeViewWrapper
    class="qm-canvas-module"
    :class="selected ? 'qm-canvas-block--selected' : ''"
    data-qm-node="question_details"
    :data-node-id="props.node.attrs.nodeId"
    data-paper-block
  >
    <PaperBlockActions v-bind="props" />
    <div class="qm-canvas-module__head" contenteditable="false">
      <span class="qm-canvas-module__label">解析模块</span>
      <el-input
        :model-value="title"
        :disabled="!state.canEdit"
        size="small"
        class="!w-48"
        placeholder="标题（可选）"
        maxlength="128"
        @update:model-value="setTitle"
      />
      <el-radio-group
        :model-value="scope"
        :disabled="!state.canEdit"
        size="small"
        @update:model-value="(v: ModuleScopeValue) => setScope(v)"
      >
        <el-radio-button value="all">全部题目</el-radio-button>
        <el-radio-button value="before">置于卷末</el-radio-button>
      </el-radio-group>
    </div>
    <NodeViewContent class="qm-canvas-module__body" />
  </NodeViewWrapper>
</template>

<style scoped>
.qm-canvas-module {
  padding: 0.5rem 0.75rem;
  border: 1px dashed var(--el-border-color);
  border-radius: 4px;
  background: var(--el-fill-color-lighter);
}
.qm-canvas-module__head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
  margin-bottom: 0.25rem;
}
.qm-canvas-module__label {
  font-size: 0.875rem;
  font-weight: 500;
}
.qm-canvas-module__body {
  outline: none;
}
</style>
