<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { knowledgePointApi } from '@/api'
import type { KnowledgePoint } from '@/api/types'
import PageHeader from '@/components/ui/PageHeader.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useSubjectStore } from '@/stores/subject'

const subject = useSubjectStore()

const loading = ref(false)
const tree = ref<KnowledgePoint[]>([])
const vectorStatus = ref<{
  total: number
  indexed: number
  missing: number
  ready: boolean
  model_name: string
  needs_rebuild: boolean
  message: string
} | null>(null)
const reindexing = ref(false)

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const form = reactive({ name: '', description: '', parent_id: null as number | null })

const canManage = computed(() => subject.can('manage_subject'))

onMounted(load)
watch(() => subject.currentId, load)

async function load() {
  if (!subject.currentId) return
  loading.value = true
  try {
    const tasks: Promise<unknown>[] = [
      knowledgePointApi.tree(subject.currentId).then((data) => {
        tree.value = data
      }),
    ]
    // 索引状态依赖向量模型配置，未配置时后端会返回明确说明而不是报错。
    tasks.push(
      knowledgePointApi
        .vectorStatus(subject.currentId)
        .then((data) => {
          vectorStatus.value = data
        })
        .catch(() => {
          vectorStatus.value = null
        }),
    )
    await Promise.all(tasks)
  } finally {
    loading.value = false
  }
}

function openCreate(parent: KnowledgePoint | null) {
  editingId.value = null
  form.name = ''
  form.description = ''
  form.parent_id = parent?.id ?? null
  dialogVisible.value = true
}

function openEdit(node: KnowledgePoint) {
  editingId.value = node.id
  form.name = node.name
  form.description = node.description
  form.parent_id = node.parent_id
  dialogVisible.value = true
}

async function onSubmit() {
  if (!subject.currentId) return
  if (!form.name.trim()) {
    ElMessage.warning('名称不能为空')
    return
  }
  if (editingId.value) {
    await knowledgePointApi.update(editingId.value, {
      name: form.name.trim(),
      description: form.description,
    })
    ElMessage.success('已保存')
  } else {
    await knowledgePointApi.create(subject.currentId, {
      name: form.name.trim(),
      description: form.description,
      parent_id: form.parent_id,
    })
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  await load()
}

async function remove(node: KnowledgePoint) {
  try {
    await ElMessageBox.confirm(
      `确定删除「${node.name}」吗？其下的子知识点也会一并删除。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await knowledgePointApi.remove(node.id)
  ElMessage.success('已删除')
  await load()
}

async function reindex(force: boolean) {
  if (!subject.currentId) return
  reindexing.value = true
  try {
    const result = await knowledgePointApi.reindex(subject.currentId, force)
    ElMessage.success(
      `索引完成：新建 ${result.indexed} 个，跳过 ${result.skipped} 个` +
        (result.failed ? `，失败 ${result.failed} 个` : '') +
        `，耗时 ${result.elapsed_ms}ms`,
    )
    await load()
  } finally {
    reindexing.value = false
  }
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="知识点" description="按学科维护知识点树。题目与试卷引用这里的节点，取消勾选的节点不会出现在组卷搜索里。" />

    <!-- 向量索引状态 -->
    <el-alert
      v-if="vectorStatus"
      :type="vectorStatus.ready ? 'success' : 'warning'"
      :closable="false"
      show-icon
    >
      <template #title>
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ vectorStatus.message }}</span>
          <span v-if="canManage" class="ml-4 shrink-0">
            <el-button
              size="small"
              :loading="reindexing"
              :type="vectorStatus.needs_rebuild ? 'primary' : 'default'"
              @click="reindex(vectorStatus.needs_rebuild)"
            >
              {{ vectorStatus.needs_rebuild ? '建立索引' : '重新索引' }}
            </el-button>
          </span>
        </div>
      </template>
      <div class="text-xs">
        已索引 {{ vectorStatus.indexed }} / {{ vectorStatus.total }}
        <template v-if="vectorStatus.model_name"> · 模型：{{ vectorStatus.model_name }}</template>
      </div>
    </el-alert>

    <el-card shadow="never">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">知识点树</span>
          <el-button v-if="canManage" type="primary" size="small" @click="openCreate(null)">
            <el-icon class="mr-1"><Plus /></el-icon>新建一级知识点
          </el-button>
        </div>
      </template>

      <TableSkeleton v-if="loading" :rows="6" :cols="6" />
      <el-table v-else
        :data="tree"
        row-key="id"
        default-expand-all
        :tree-props="{ children: 'children' }"
      >
        <el-table-column prop="name" label="名称" min-width="260" />
        <el-table-column prop="description" label="说明" min-width="200" show-overflow-tooltip />
        <el-table-column label="题目数" width="90" align="right">
          <template #default="{ row }">
            <span class="num text-ink-muted">{{ row.question_count }}</span>
          </template>
        </el-table-column>
        <el-table-column v-if="canManage" label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button text size="small" @click="openCreate(row)">加子级</el-button>
            <el-button text size="small" @click="openEdit(row)">编辑</el-button>
            <el-button text size="small" type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
        <template #empty>
          <EmptyState
            :description="subject.currentId ? '还没有知识点' : '请先选择学科'"
            :hint="
              subject.currentId
                ? '知识点是题目与试卷的检索维度。智能录题会尝试自动匹配，手工补全能明显提高后续的检索质量。'
                : ''
            "
          />
        </template>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑知识点' : '新建知识点'" width="480px">
      <el-form label-width="70px">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如：函数单调性" maxlength="100" show-word-limit />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" type="textarea" :rows="3" placeholder="选填" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="onSubmit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
