<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  adminImportApi,
  type AdminImportKind,
  type AdminImportRowError,
  type KnowledgePointImportMode,
  type KnowledgePointImportPreflight,
} from '@/api/adminImport'

const props = defineProps<{
  modelValue: boolean
  kind: AdminImportKind
  subjectId?: number
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  imported: []
}>()

const fileInput = ref<HTMLInputElement | null>(null)
const file = ref<File | null>(null)
const mode = ref<KnowledgePointImportMode>('incremental')
const importing = ref(false)
const preflight = ref<KnowledgePointImportPreflight | null>(null)
const result = ref<{
  status: string
  total?: number
  created?: number
  skipped?: number
  failed?: number
  errors?: AdminImportRowError[]
} | null>(null)

const title = computed(() => {
  if (props.kind === 'users') return '批量导入用户'
  if (props.kind === 'tags') return '批量导入标签'
  return '批量导入知识点'
})

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit('update:modelValue', value),
})

watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return
    file.value = null
    result.value = null
    mode.value = 'incremental'
    preflight.value = null
    if (props.kind === 'knowledge_points' && props.subjectId) {
      try {
        preflight.value = await adminImportApi.knowledgePointPreflight(props.subjectId)
      } catch {
        preflight.value = null
      }
    }
  },
)

function pickFile(event: Event) {
  const input = event.target as HTMLInputElement
  const selected = input.files?.[0] ?? null
  input.value = ''
  if (!selected) return
  if (!selected.name.toLowerCase().endsWith('.xlsx')) {
    ElMessage.warning('仅支持 .xlsx 文件')
    return
  }
  file.value = selected
}

async function downloadTemplate() {
  await adminImportApi.downloadTemplate(props.kind, props.subjectId)
}

async function startImport() {
  if (!file.value) {
    ElMessage.warning('请先选择 .xlsx 文件')
    return
  }
  importing.value = true
  try {
    if (props.kind === 'users') {
      result.value = await adminImportApi.importUsers(file.value)
    } else if (!props.subjectId) {
      throw new Error('缺少学科')
    } else if (props.kind === 'tags') {
      result.value = await adminImportApi.importTags(props.subjectId, file.value)
    } else {
      result.value = await adminImportApi.importKnowledgePoints(
        props.subjectId,
        file.value,
        mode.value,
      )
      preflight.value = await adminImportApi.knowledgePointPreflight(props.subjectId)
    }
    emit('imported')
    ElMessage.success('导入完成')
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <el-dialog v-model="visible" :title="title" width="680px" append-to-body>
    <div class="space-y-4">
      <el-alert v-if="preflight" type="warning" :closable="false" show-icon>
        <template #title>重建影响面</template>
        当前有 {{ preflight.existing_count }} 个知识点，关联 {{ preflight.affected_questions }} 道题。
        重建会解除这些题目的知识点关联，并使用文件中的层级替换现有知识点。
      </el-alert>

      <div class="flex flex-wrap items-center gap-2">
        <el-button @click="downloadTemplate">下载模板</el-button>
        <el-button @click="fileInput?.click()">选择 .xlsx 文件</el-button>
        <span class="text-sm text-ink-muted">{{ file?.name || '尚未选择文件' }}</span>
        <input
          ref="fileInput"
          class="hidden"
          type="file"
          accept=".xlsx"
          @change="pickFile"
        />
      </div>

      <el-radio-group v-if="kind === 'knowledge_points'" v-model="mode">
        <el-radio value="incremental">增量导入</el-radio>
        <el-radio value="rebuild">重建知识点树</el-radio>
      </el-radio-group>

      <div v-if="result" class="rounded border border-hairline p-3">
        <div class="flex flex-wrap gap-2">
          <el-tag :type="result.status === 'failed' ? 'danger' : result.status === 'partial' ? 'warning' : 'success'">
            {{ result.status === 'failed' ? '失败' : result.status === 'partial' ? '部分成功' : '成功' }}
          </el-tag>
          <el-tag v-if="result.created !== undefined" type="success">新建 {{ result.created }}</el-tag>
          <el-tag v-if="result.skipped" type="info">跳过 {{ result.skipped }}</el-tag>
          <el-tag v-if="result.failed" type="danger">失败 {{ result.failed }}</el-tag>
        </div>

        <el-table v-if="result.errors?.length" :data="result.errors" size="small" class="mt-3" max-height="240">
          <el-table-column prop="row" label="行号" width="80" />
          <el-table-column prop="message" label="错误" />
        </el-table>
      </div>
    </div>

    <template #footer>
      <el-button @click="visible = false">关闭</el-button>
      <el-button type="primary" :loading="importing" :disabled="!file" @click="startImport">
        开始导入
      </el-button>
    </template>
  </el-dialog>
</template>
