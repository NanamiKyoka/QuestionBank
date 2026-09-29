<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { ApiError } from '@/api/http'
import { paperApi, type AppendQuestionsResult, type PaperBrief, type PaperScope } from '@/api/paper'
import { useSubjectStore } from '@/stores/subject'

/**
 * 选题目标选择器：把一批题目追加进某份已有试卷，或边建边加。
 *
 * 后端只有整树覆盖的 `PUT /papers/:id/nodes`，没有"批量加题"端点，
 * 所以追加在 `paperApi.appendQuestions` 里完成（读整树 → 拼接 → 带
 * expected_revision 写回）。这里只负责**选目标**与**把失败说清楚**：
 *
 *  - 409（试卷已被他人修改）不能自动重试。重试等于拿一份过期的整树覆盖
 *    回去，正是乐观锁要防的事；这里如实告诉用户"别人改过，请重开一次"。
 *  - 403 / 404 由接口层弹提示，这里不再重复弹。
 */
const open = defineModel<boolean>({ default: false })

const props = withDefaults(
  defineProps<{
    /** 题目所属学科。缺省时用当前学科。 */
    subjectId?: number | null
    /** 要加入的题目 id 列表。 */
    questionIds: number[]
    /** 弹窗标题。 */
    title?: string
  }>(),
  { subjectId: null, title: '加入试卷' },
)

const emit = defineEmits<{
  added: [result: AppendQuestionsResult]
}>()

const subject = useSubjectStore()

const loading = ref(false)
const submitting = ref(false)
const papers = ref<PaperBrief[]>([])
const keyword = ref('')
const selected = ref<PaperBrief | null>(null)

/** 新建试卷的标题。留空则用「加入试题」做默认名。 */
const newTitle = ref('')

const effectiveSubjectId = computed<number | null>(() => props.subjectId ?? subject.currentId)

const filtered = computed(() => {
  const q = keyword.value.trim().toLowerCase()
  if (!q) return papers.value
  return papers.value.filter((p) => p.title.toLowerCase().includes(q))
})

const defaultNewTitle = computed(() => {
  const now = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `试题 ${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
})

/** 试卷列表是给"挑目标"用的：个人与共享都列出来，按最近更新排序。 */
async function loadPapers() {
  const subjectId = effectiveSubjectId.value
  if (!subjectId) {
    papers.value = []
    return
  }
  loading.value = true
  try {
    const scopes: PaperScope[] = ['personal', 'shared']
    const results = await Promise.all(
      scopes.map((scope) =>
        paperApi
          .list(subjectId, { scope, page: 1, page_size: 50 })
          .then((r) => r.list ?? [])
          .catch(() => []),
      ),
    )
    papers.value = results
      .flat()
      .slice()
      .sort((a, b) => (a.updated_at < b.updated_at ? 1 : -1))
  } finally {
    loading.value = false
  }
}

watch(open, (isOpen) => {
  if (!isOpen) return
  selected.value = null
  keyword.value = ''
  newTitle.value = ''
  void loadPapers()
})

async function addTo(paper: PaperBrief) {
  submitting.value = true
  try {
    const result = await paperApi.appendQuestions(paper.id, props.questionIds)
    ElMessage.success(`已把 ${result.added} 道题加入《${result.paper_title}》`)
    emit('added', result)
    open.value = false
  } catch (error) {
    if (error instanceof ApiError && error.isConflict) {
      // 409 走乐观锁，接口层按约定不弹全局提示（免得和这里重复）。
      ElMessage.warning('这份试卷刚被他人修改过，为避免覆盖对方的改动，已取消本次追加。请重新打开选择器再试一次。')
    }
    // 其余错误已由接口层统一提示。
  } finally {
    submitting.value = false
  }
}

async function createAndAdd() {
  const subjectId = effectiveSubjectId.value
  if (!subjectId) return
  const title = newTitle.value.trim() || defaultNewTitle.value
  submitting.value = true
  try {
    const paper = await paperApi.create(subjectId, { title })
    const result = await paperApi.appendQuestions(paper.id, props.questionIds)
    ElMessage.success(`已创建《${title}》并加入 ${result.added} 道题`)
    emit('added', result)
    open.value = false
  } catch (error) {
    if (error instanceof ApiError && error.isConflict) {
      ElMessage.warning('这份试卷刚被他人修改过，已取消本次追加。')
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <el-dialog v-model="open" :title="title" width="560px" :close-on-click-modal="false">
    <div class="mb-2 text-sm text-gray-500">
      将 <span class="font-medium text-gray-700">{{ questionIds.length }}</span> 道题追加到所选试卷末尾
    </div>

    <el-input v-model="keyword" placeholder="搜索试卷标题" clearable class="mb-2">
      <template #prefix><el-icon><Search /></el-icon></template>
    </el-input>

    <div v-loading="loading" class="max-h-[320px] overflow-y-auto">
      <div
        v-for="paper in filtered"
        :key="paper.id"
        class="mb-1 cursor-pointer rounded border p-2 transition-colors"
        :class="selected?.id === paper.id ? 'border-blue-400 bg-blue-50' : 'border-gray-200 hover:bg-gray-50'"
        @click="selected = paper"
      >
        <div class="flex items-center gap-2">
          <el-radio :model-value="selected?.id === paper.id" @change="selected = paper" />
          <span class="min-w-0 flex-1 truncate text-sm">{{ paper.title }}</span>
          <el-tag v-if="paper.scope === 'shared'" size="small" type="info">共享</el-tag>
          <el-tag size="small" effect="plain">{{ paper.question_count }} 题</el-tag>
        </div>
      </div>

      <el-empty
        v-if="!loading && filtered.length === 0"
        :description="keyword ? '没有匹配的试卷' : '还没有试卷，可以直接新建一份'"
        :image-size="56"
      />
    </div>

    <div class="mt-3 flex items-center gap-2 border-t border-gray-100 pt-3">
      <span class="shrink-0 text-xs text-gray-400">没有合适的？</span>
      <el-input
        v-model="newTitle"
        size="small"
        :placeholder="defaultNewTitle"
        maxlength="200"
        class="w-56"
        @keyup.enter="createAndAdd"
      />
      <el-button size="small" :loading="submitting" :disabled="!effectiveSubjectId" @click="createAndAdd">
        <el-icon class="mr-1"><Plus /></el-icon>新建并加入
      </el-button>
    </div>

    <template #footer>
      <el-button @click="open = false">取消</el-button>
      <el-button
        type="primary"
        :loading="submitting"
        :disabled="!selected || !effectiveSubjectId"
        @click="selected && addTo(selected)"
      >
        加入所选试卷
      </el-button>
    </template>
  </el-dialog>
</template>
