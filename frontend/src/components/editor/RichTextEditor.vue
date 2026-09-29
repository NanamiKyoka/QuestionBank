<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import StarterKit from '@tiptap/starter-kit'
import Image from '@tiptap/extension-image'
import Placeholder from '@tiptap/extension-placeholder'
import Subscript from '@tiptap/extension-subscript'
import Superscript from '@tiptap/extension-superscript'
import TextAlign from '@tiptap/extension-text-align'
// Tiptap v3 把整个表格家族统一到 extension-table 一个包，
// 不再需要分别安装 row / cell / header。
import { Table, TableCell, TableHeader, TableRow } from '@tiptap/extension-table'
import { ElMessageBox } from 'element-plus'
import { Blank, BlockMath, InlineMath } from './mathNodes'

/**
 * 富文本编辑器。
 *
 * 文档结构与后端的 RichDoc 完全一致（Tiptap JSON），因此保存时
 * 不需要任何转换——转换层是"公式渲染错位"这类问题的常见来源。
 *
 * 关于公式的编辑体验：编辑器里公式显示为等宽的 LaTeX 原文（$x^2$），
 * 而不是实时渲染的排版结果。这是刻意的取舍——实时渲染需要为每个公式
 * 挂一个节点视图，一页几十个公式时输入会明显卡顿；而录入场景下
 * "看清自己打了什么"比"看到最终排版"更重要。最终排版在预览与导出时渲染。
 */
const props = withDefaults(
  defineProps<{
    modelValue: unknown
    placeholder?: string
    minHeight?: string
  }>(),
  { placeholder: '请输入内容，公式用 $...$ 包裹', minHeight: '160px' },
)

const emit = defineEmits<{
  'update:modelValue': [value: unknown]
  /** 内容变化（含用户输入），用于外部做脏检查。 */
  change: []
}>()

const editor = useEditor({
  content: (props.modelValue as object) ?? { type: 'doc', content: [{ type: 'paragraph' }] },
  extensions: [
    StarterKit.configure({
      // 标题层级限制在 1-3 级：试卷排版用不到更深的层级，
      // 放开只会让用户排出无法导出的结构。
      heading: { levels: [1, 2, 3] },
      codeBlock: false,
    }),
    InlineMath,
    BlockMath,
    Blank,
    Image.configure({ inline: false }),
    Placeholder.configure({ placeholder: props.placeholder }),
    Subscript,
    Superscript,
    TextAlign.configure({ types: ['heading', 'paragraph'] }),
    Table.configure({ resizable: true }),
    TableRow,
    TableHeader,
    TableCell,
  ],
  onUpdate: ({ editor: instance }) => {
    emit('update:modelValue', instance.getJSON())
    emit('change')
  },
})

// 外部改值时同步进编辑器（如切换编辑的题目）。
watch(
  () => props.modelValue,
  (value) => {
    const instance = editor.value
    if (!instance) return
    const current = JSON.stringify(instance.getJSON())
    const next = JSON.stringify(value ?? {})
    // 只在确实不同的时候 setContent，否则每次输入都会被自身的 update 事件重置光标。
    if (current !== next) {
      instance.commands.setContent((value as object) ?? { type: 'doc', content: [{ type: 'paragraph' }] })
    }
  },
)

onBeforeUnmount(() => editor.value?.destroy())

/** 插入行内公式。 */
async function insertInlineMath() {
  const latex = await promptLatex('插入行内公式', '例如：x^2 + y^2 = r^2')
  if (latex === null) return
  editor.value?.chain().focus().insertContent({ type: 'inlineMath', attrs: { latex } }).run()
}

/** 插入独立公式。 */
async function insertBlockMath() {
  const latex = await promptLatex('插入独立公式', '例如：\\frac{-b \\pm \\sqrt{b^2-4ac}}{2a}')
  if (latex === null) return
  editor.value?.chain().focus().insertContent({ type: 'blockMath', attrs: { latex } }).run()
}

/** 插入填空空位。 */
function insertBlank() {
  editor.value?.chain().focus().insertContent({ type: 'blank' }).run()
}

const uploading = ref(false)

/**
 * 上传图片并插入正文。
 *
 * 走后端 /uploads/images：那里会按文件头校验真实格式、
 * 限制尺寸并拒绝 SVG（SVG 可内嵌脚本，当图片直接提供出去等于给了 XSS 机会）。
 * 因此这里不做本地格式判断——客户端校验只是体验优化，说了算的是服务端。
 */
async function pickImage(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许连续选择同一个文件
  if (!file) return

  uploading.value = true
  try {
    const { uploadApi } = await import('@/api/ai')
    const saved = await uploadApi.image(file)
    editor.value?.chain().focus().setImage({ src: saved.url }).run()
  } catch {
    // 具体原因（过大、格式不支持）由接口层统一提示
  } finally {
    uploading.value = false
  }
}

async function promptLatex(title: string, placeholder: string): Promise<string | null> {
  try {
    const { value } = await ElMessageBox.prompt('请输入 LaTeX 源码（不需要写 $ 符号）', title, {
      confirmButtonText: '插入',
      cancelButtonText: '取消',
      inputPlaceholder: placeholder,
      inputValidator: (v) => (v && v.trim() ? true : '公式不能为空'),
    })
    return value.trim()
  } catch {
    return null // 用户取消
  }
}


defineExpose({
  /** 让外部能取到编辑器实例（如插入内容、聚焦）。 */
  editor,
})
</script>

<template>
  <div class="rounded border border-gray-300 bg-white">
    <div
      v-if="editor"
      class="flex flex-wrap items-center gap-0.5 border-b border-gray-200 bg-gray-50 px-2 py-1"
    >
      <el-button-group>
        <el-button
          size="small"
          :type="editor.isActive('bold') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleBold().run()"
        >
          <strong>B</strong>
        </el-button>
        <el-button
          size="small"
          :type="editor.isActive('italic') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleItalic().run()"
        >
          <em>I</em>
        </el-button>
        <el-button
          size="small"
          :type="editor.isActive('underline') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleUnderline().run()"
        >
          <u>U</u>
        </el-button>
      </el-button-group>

      <el-button-group class="ml-1">
        <el-button
          size="small"
          :type="editor.isActive('superscript') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleSuperscript().run()"
        >
          x²
        </el-button>
        <el-button
          size="small"
          :type="editor.isActive('subscript') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleSubscript().run()"
        >
          x₂
        </el-button>
      </el-button-group>

      <el-divider direction="vertical" />

      <el-button size="small" @click="insertInlineMath">$ 行内公式</el-button>
      <el-button size="small" @click="insertBlockMath">∑ 独立公式</el-button>
      <el-button size="small" @click="insertBlank">＿ 填空位</el-button>

      <el-divider direction="vertical" />

      <el-button-group>
        <el-button
          size="small"
          :type="editor.isActive('bulletList') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleBulletList().run()"
        >
          • 列表
        </el-button>
        <el-button
          size="small"
          :type="editor.isActive('orderedList') ? 'primary' : 'default'"
          @click="editor.chain().focus().toggleOrderedList().run()"
        >
          1. 列表
        </el-button>
        <el-button
          size="small"
          :type="editor.isActive('table') ? 'primary' : 'default'"
          @click="
            editor.chain().focus().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()
          "
        >
          ▦ 表格
        </el-button>
      </el-button-group>


      <label class="ml-1 inline-block">
        <input
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp"
          class="hidden"
          @change="pickImage"
        />
        <el-button size="small" :loading="uploading" tag="span">🖼 图片</el-button>
      </label>

      <el-button size="small" class="ml-auto" @click="editor.chain().focus().clearNodes().unsetAllMarks().run()">
        清除格式
      </el-button>
    </div>

    <EditorContent
      :editor="editor"
      class="prose-editor px-3 py-2"
      :style="{ minHeight: props.minHeight }"
    />
  </div>
</template>

<style scoped>
.prose-editor :deep(.ProseMirror) {
  outline: none;
  min-height: inherit;
}
.prose-editor :deep(.ProseMirror p) {
  margin: 0.35rem 0;
}
.prose-editor :deep(.ProseMirror p.is-editor-empty:first-child::before) {
  content: attr(data-placeholder);
  color: var(--el-text-color-placeholder);
  float: left;
  height: 0;
  pointer-events: none;
}
.prose-editor :deep(.ProseMirror table) {
  border-collapse: collapse;
  width: 100%;
  margin: 0.5rem 0;
}
.prose-editor :deep(.ProseMirror th),
.prose-editor :deep(.ProseMirror td) {
  border: 1px solid var(--el-border-color);
  padding: 0.25rem 0.5rem;
  min-width: 3rem;
}
.prose-editor :deep(img) {
  max-width: 100%;
}
.prose-editor :deep(.ProseMirror-selectednode) {
  outline: 2px solid var(--el-color-primary);
}
</style>
