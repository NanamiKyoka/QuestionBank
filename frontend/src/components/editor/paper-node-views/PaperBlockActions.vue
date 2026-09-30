<script setup lang="ts">
import { computed } from 'vue'
import { nodeViewProps } from '@tiptap/vue-3'
import { ElMessage } from 'element-plus'
import { canMoveTopLevelBlock, deleteTopLevelBlock, moveTopLevelBlock } from '../paperBlockOps'
import { paperCanvasStateOf, useEditorTick } from './canvasState'

/**
 * 每个块右上角的操作条：上移 / 下移 / 删除。
 *
 * 为什么不做成"拖拽排序"：拖拽是这个迁移的下一步（要引入拖拽手柄扩展），
 * 但**在拖拽到位之前不能先丢掉移动能力**——旧的卡片列表有上移下移，
 * 画布若只有拖拽、拖拽又还没做，切换过去就是功能倒退。
 * 所以先用按钮把"能移动"这件事补齐，拖拽是之后的增量。
 *
 * 按钮的禁用态读了下标，而下标属于编辑器状态，因此这里订阅了事务计数
 * （见 useEditorTick 的注释）：别的块被删掉之后，这一块该立刻变得能点。
 */
const props = defineProps(nodeViewProps)

const state = computed(() => paperCanvasStateOf(props.editor))
const tick = useEditorTick(props.editor)

const pos = computed(() => {
  // tick 只是为了让这个 computed 跟着事务重算，值本身不用。
  void tick.value
  const raw = props.getPos()
  return typeof raw === 'number' ? raw : -1
})

const canMoveUp = computed(() => canMoveTopLevelBlock(props.editor, pos.value, -1))
const canMoveDown = computed(() => canMoveTopLevelBlock(props.editor, pos.value, 1))

function move(delta: number) {
  if (!state.value.canEdit) return
  if (!moveTopLevelBlock(props.editor, pos.value, delta)) {
    ElMessage.info(delta < 0 ? '已经在最前面了' : '已经在最后面了')
  }
}

function remove() {
  if (!state.value.canEdit) return
  if (!deleteTopLevelBlock(props.editor, pos.value)) {
    ElMessage.warning('解析模块请在工具栏上移除')
  }
}
</script>

<template>
  <div v-if="state.canEdit" class="qm-block-actions" contenteditable="false">
    <el-button text size="small" :disabled="!canMoveUp" title="上移" @click="move(-1)">
      <el-icon><Top /></el-icon>
    </el-button>
    <el-button text size="small" :disabled="!canMoveDown" title="下移" @click="move(1)">
      <el-icon><Bottom /></el-icon>
    </el-button>
    <el-button text size="small" type="danger" title="删除" @click="remove">
      <el-icon><Delete /></el-icon>
    </el-button>
  </div>
</template>

<style scoped>
.qm-block-actions {
  position: absolute;
  top: 0.25rem;
  right: 0.25rem;
  display: flex;
  gap: 0.125rem;
  opacity: 0;
  transition: opacity 0.15s;
  z-index: 1;
}
/* 悬停或选中时才出现，平时不挡内容。 */
:deep(.qm-canvas-block:hover) .qm-block-actions,
:deep(.qm-canvas-block--selected) .qm-block-actions,
:deep(.qm-canvas-question:hover) .qm-block-actions,
:deep(.qm-canvas-heading:hover) .qm-block-actions,
:deep(.qm-canvas-answerspace:hover) .qm-block-actions,
:deep(.qm-canvas-module:hover) .qm-block-actions {
  opacity: 1;
}
</style>
