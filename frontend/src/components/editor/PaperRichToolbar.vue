<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Editor } from '@tiptap/core'
import {
  clearFormat,
  insertBlank,
  insertTable,
  requestBlockMath,
  requestInlineMath,
  selectionInRichBlock,
  uploadAndInsertImage,
} from './richEditorActions'
import { useEditorTick } from './paper-node-views/canvasState'
import EditorFormatControls from './EditorFormatControls.vue'

/**
 * 画布上富文本块的工具栏。
 *
 * 它作用在**画布那一份编辑器**上，而不是每个块各挂一个编辑器实例——
 * 画布的全部意义就是"整篇是一份文档"，给每个块再开一个编辑器会把
 * 光标、撤销栈、拖拽全部拆散。
 *
 * 只在选区落在说明文字或标题里时出现：题目块有自己的题号/分值控件，
 * 解析条目是只读的，在那些地方露出公式按钮只会让人以为点了没反应。
 */
const props = defineProps<{
  editor: Editor | null | undefined
  /** 没有编辑权限时整条工具栏不渲染。 */
  canEdit: boolean
}>()

// 选区属于编辑器状态，工具栏的显示与否要跟着事务变，
// 因此订阅事务计数（同 PaperBlockActions 的理由）。
const tick = useEditorTick(props.editor)

const visible = computed(() => {
  void tick.value
  return props.canEdit && selectionInRichBlock(props.editor)
})

const uploading = ref(false)

function run(action: (editor: Editor) => unknown) {
  const instance = props.editor
  if (!instance) return
  void action(instance)
}

async function pickImage(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 先清空：否则连续选同一个文件不会再触发 change。
  input.value = ''
  if (!file || !props.editor) return
  uploading.value = true
  try {
    await uploadAndInsertImage(props.editor, file)
  } catch {
    // 具体原因（过大、格式不支持）由接口层统一提示
  } finally {
    uploading.value = false
  }
}
</script>

<template>
  <div v-if="visible && editor" class="qm-richtoolbar" contenteditable="false">
    <el-button-group>
      <el-button
        size="small"
        :type="editor.isActive('bold') ? 'primary' : 'default'"
        title="加粗"
        @click="run((e) => e.chain().focus().toggleBold().run())"
      >
        <strong>B</strong>
      </el-button>
      <el-button
        size="small"
        :type="editor.isActive('italic') ? 'primary' : 'default'"
        title="斜体"
        @click="run((e) => e.chain().focus().toggleItalic().run())"
      >
        <em>I</em>
      </el-button>
      <el-button
        size="small"
        :type="editor.isActive('underline') ? 'primary' : 'default'"
        title="下划线"
        @click="run((e) => e.chain().focus().toggleUnderline().run())"
      >
        <u>U</u>
      </el-button>
    </el-button-group>

    <el-button-group class="ml-1">
      <el-button
        size="small"
        :type="editor.isActive('superscript') ? 'primary' : 'default'"
        title="上标"
        aria-label="上标"
        @click="run((e) => e.chain().focus().toggleSuperscript().run())"
      >
        <el-icon><Top /></el-icon>
      </el-button>
      <el-button
        size="small"
        :type="editor.isActive('subscript') ? 'primary' : 'default'"
        title="下标"
        aria-label="下标"
        @click="run((e) => e.chain().focus().toggleSubscript().run())"
      >
        <el-icon><Bottom /></el-icon>
      </el-button>
    </el-button-group>

    <el-divider direction="vertical" />

    <EditorFormatControls :editor="editor" />

    <el-divider direction="vertical" />

    <!-- 公式与填空：题库的核心内容形态，绝不能少 -->
    <el-button size="small" title="插入行内公式" @click="run(requestInlineMath)">行内公式</el-button>
    <el-button size="small" title="插入独立公式" @click="run(requestBlockMath)">独立公式</el-button>
    <el-button size="small" title="插入填空位" @click="run(insertBlank)">填空位</el-button>

    <el-divider direction="vertical" />

    <el-button-group>
      <el-button
        size="small"
        :type="editor.isActive('bulletList') ? 'primary' : 'default'"
        title="无序列表"
        aria-label="无序列表"
        @click="run((e) => e.chain().focus().toggleBulletList().run())"
      >
        <el-icon><List /></el-icon>
      </el-button>
      <el-button
        size="small"
        :type="editor.isActive('orderedList') ? 'primary' : 'default'"
        title="有序列表"
        aria-label="有序列表"
        @click="run((e) => e.chain().focus().toggleOrderedList().run())"
      >
        <el-icon><Finished /></el-icon>
      </el-button>
      <el-button size="small" title="插入表格" aria-label="插入表格" @click="run(insertTable)">
        <el-icon><Grid /></el-icon>
      </el-button>
    </el-button-group>

    <label class="ml-1 inline-block">
      <!-- 不用 hidden 类：那样输入框进不了 Tab 序，纯键盘用户就传不了图。
           sr-only 平时裁成 0 尺寸，键盘聚焦时现回原生控件（鼠标点了不会突然蹦出来），
           焦点看得见，Enter 就能选文件。 -->
      <input
        type="file"
        accept="image/png,image/jpeg,image/gif,image/webp"
        class="sr-only focus-visible:not-sr-only"
        aria-label="插入图片"
        @change="pickImage"
      />
      <el-button size="small" :loading="uploading" tag="span" title="插入图片" aria-label="插入图片">
        <el-icon><Picture /></el-icon>
      </el-button>
    </label>

    <el-button size="small" class="ml-auto" @click="run(clearFormat)">清除格式</el-button>
  </div>
</template>

<style scoped>
.qm-richtoolbar {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.125rem;
  padding: 0.25rem 0.5rem;
  margin-bottom: 0.5rem;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  background: var(--el-fill-color-light);
}
</style>
