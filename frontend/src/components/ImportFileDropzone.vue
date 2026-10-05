<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'

/**
 * 导入文件的投放区：点选、拖拽、粘贴三种入口。
 *
 * 三个入口是刻意并存的。真实场景里这三件事本来就会混着发生——
 * 老师从文件夹里拖一个 .docx 过来，顺手把手机拍的卷子截图 Ctrl+V 进来；
 * 只留一个入口就等于逼用户先开 Word 再复制粘贴，
 * 排版里的图和表在这一步就全丢了。
 *
 * 图片不进这条通道：`/imports/upload` 只吃文档，截图走的是「上传图片 → 图片地址」
 * 那条已有链路（见 ImportView），所以这里把图片原样交回上层，不擅自转格式。
 */
const props = withDefaults(
  defineProps<{
    /** 当前待导入的文档；null 表示还没选。 */
    modelValue: File | null
    disabled?: boolean
    /** 允许的扩展名（同时用于 <input accept> 与拖拽校验）。 */
    accept?: string
  }>(),
  { disabled: false, accept: '.docx,.md,.txt,.pdf,.zip' },
)

const emit = defineEmits<{
  'update:modelValue': [file: File | null]
  /** 拖进来/粘进来的是图片：交给上层转交图片识别。 */
  image: [file: File]
  /** 一次拿到多份文档：交给上层决定同步识别还是整批排队。 */
  batch: [files: File[]]
}>()

const picker = ref<HTMLInputElement | null>(null)
const folderPicker = ref<HTMLInputElement | null>(null)
const dragging = ref(false)

const extensions = computed(() =>
  props.accept
    .split(',')
    .map((item) => item.trim().toLowerCase())
    .filter(Boolean),
)

/** 给用户看的格式清单：DOCX / MD / TXT / ZIP。 */
const formatHint = computed(() =>
  extensions.value.map((ext) => (ext.startsWith('.') ? ext.slice(1) : ext).toUpperCase()).join(' / '),
)

function sizeLabel(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}

/**
 * 把一次选择/拖拽拿到的文件分流。
 *
 * 单份仍走 `update:modelValue`（"我现在就要看结果"），
 * 多份走 `batch`——整个文件夹的卷子交给后台 worker 依次排队，
 * 比逐个提交后自己盯进度现实得多。
 * 图片不进这条通道：`/imports/upload` 只吃文档，原样交回上层转图片识别。
 */
function dispatchFiles(files: File[]): void {
  const docs: File[] = []
  for (const file of files) {
    if (file.type.startsWith('image/')) {
      emit('image', file)
      continue
    }
    const name = file.name.toLowerCase()
    if (!extensions.value.some((ext) => name.endsWith(ext))) {
      ElMessage.warning(`不支持的文件类型：${file.name}。当前只接受 ${formatHint.value}`)
      continue
    }
    docs.push(file)
  }
  if (!docs.length) return
  if (docs.length === 1) {
    emit('update:modelValue', docs[0])
    return
  }
  emit('batch', docs)
}

function collect(input: HTMLInputElement | null): File[] {
  const files = Array.from(input?.files ?? [])
  if (input) input.value = '' // 允许重选同一个文件
  return files
}

function onPick(event: Event) {
  dispatchFiles(collect(event.target as HTMLInputElement))
}

function onPickFolder(event: Event) {
  dispatchFiles(collect(event.target as HTMLInputElement))
}

function onDrop(event: DragEvent) {
  dragging.value = false
  if (props.disabled) return
  dispatchFiles(Array.from(event.dataTransfer?.files ?? []))
}

function onPaste(event: ClipboardEvent) {
  if (props.disabled) return
  for (const item of Array.from(event.clipboardData?.items ?? [])) {
    if (!item.type.startsWith('image/')) continue
    const blob = item.getAsFile()
    if (blob) emit('image', new File([blob], `pasted-${Date.now()}.png`, { type: blob.type }))
    return
  }
}

function clear() {
  emit('update:modelValue', null)
}
</script>

<template>
  <div
    tabindex="0"
    role="button"
    aria-label="拖放文件到此处上传，或按 Enter 打开文件选择"
    class="rounded-lg border-2 border-dashed px-6 py-8 text-center transition-colors focus:outline-none"
    :class="[
      dragging ? 'border-accent bg-accent-soft/60' : 'border-hairline-strong bg-surface-sunken/40',
      disabled ? 'pointer-events-none opacity-60' : 'hover:border-accent-border',
    ]"
    @dragover.prevent="dragging = true"
    @dragleave.prevent="dragging = false"
    @drop.prevent="onDrop"
    @paste="onPaste"
    @keydown.enter.prevent="picker?.click()"
    @keydown.space.prevent="picker?.click()"
  >
    <input ref="picker" type="file" multiple :accept="accept" class="hidden" :disabled="disabled" @change="onPick" />
    <!-- 选整个文件夹：webkitdirectory 是事实标准，选中的就是目录里的全部文件。 -->
    <input
      ref="folderPicker"
      type="file"
      webkitdirectory
      class="hidden"
      :disabled="disabled"
      @change="onPickFolder"
    />

    <template v-if="modelValue">
      <div class="flex flex-col items-center gap-2">
        <el-icon :size="32" class="text-accent-text"><Document /></el-icon>
        <div class="text-sm font-medium" :title="modelValue.name">{{ modelValue.name }}</div>
        <div class="text-xs text-ink-subtle">
          {{ sizeLabel(modelValue.size) }} · {{ extensions[0]?.slice(1).toUpperCase() }}
        </div>
        <div class="mt-2 flex gap-2">
          <el-button size="small" :disabled="disabled" @click="picker?.click()">换一个文件</el-button>
          <el-button size="small" text type="danger" :disabled="disabled" @click="clear">移除</el-button>
        </div>
      </div>
    </template>

    <template v-else>
      <div class="flex flex-col items-center gap-2">
        <el-icon :size="36" class="text-ink-subtle/60"><UploadFilled /></el-icon>
        <div class="flex flex-wrap justify-center gap-2">
          <el-button type="primary" plain :disabled="disabled" @click="picker?.click()">
            <el-icon class="mr-1"><FolderOpened /></el-icon>
            选择文件
          </el-button>
          <el-button plain :disabled="disabled" @click="folderPicker?.click()">
            <el-icon class="mr-1"><Files /></el-icon>
            选择文件夹
          </el-button>
        </div>
        <div class="text-xs text-ink-muted">
          或把文件拖到这里，也可以直接 Ctrl+V 粘贴截图；一次选中多份会提示整批排队。
        </div>
        <div class="mt-1 flex flex-wrap items-center justify-center gap-2 text-xs text-ink-subtle">
          <el-tag v-for="ext in extensions" :key="ext" size="small" type="info" effect="plain">
            {{ ext.slice(1).toUpperCase() }}
          </el-tag>
        </div>
        <div class="text-xs text-ink-subtle">
          {{ formatHint }} 均可；.zip 会一次导入压缩包里的多份文档。
        </div>
        <div class="text-xs text-ink-subtle">
          .pdf 会先渲染成页图再由模型识别：公式与几何图形按排版原样保留，比读文字层准确得多；
          代价是比 .md/.txt 慢（模型要逐页看图）。扫描件同样适用。
        </div>
      </div>
    </template>
  </div>
</template>
