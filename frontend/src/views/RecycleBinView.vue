<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { questionApi } from '@/api'
import { paperApi, type PaperBrief, type PaperScope } from '@/api/paper'
import type { QuestionBrief, QuestionSort, QuestionType } from '@/api/types'
import RichContent from '@/components/RichContent.vue'
import DifficultyStars from '@/components/ui/DifficultyStars.vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { useSubjectStore } from '@/stores/subject'

/**
 * 回收站。
 *
 * 题库页与试卷页的删除确认框都承诺过"删除后可在回收站中恢复"——
 * 这个页面就是那句承诺的落点。两边的删除都是软删除，数据仍在库里，
 * 误删可以原样恢复。
 *
 * 后端现状（只读记录）：
 *   - 列表：题目与试卷都支持 `only_deleted=true`，只返回已删除的记录
 *     （backend/internal/repository/question.go:172、paper.go:51）。
 *   - 恢复：`POST /questions/:id/restore`、`POST /papers/:id/restore`，
 *     内部走 Unscoped 的 GetDeleted，所以对已删除的记录能正常恢复（422 表示它其实没被删）。
 *   - 彻底删除：两个资源都没有硬删接口，因此这里只有"恢复"。
 */

const subject = useSubjectStore()

const tab = ref<'questions' | 'papers'>('questions')

const questionLoading = ref(false)
const questionRows = ref<QuestionBrief[]>([])
const questionTotal = ref(0)

const paperLoading = ref(false)
const paperRows = ref<PaperBrief[]>([])
const paperTotal = ref(0)

/** 题目筛选。回收站沿用题库页最常用的两个维度：关键词与题型。 */
const questionQuery = reactive({
  keyword: '',
  type: '' as QuestionType | '',
  // 回收站按删除时间倒序最有用：软删除会同时写 updated_at，
  // 所以"最近删掉的"就是最近更新的那一批。
  sort: 'updated_desc' as QuestionSort,
  page: 1,
  page_size: 20,
})

const paperQuery = reactive({
  keyword: '',
  scope: '' as PaperScope | '',
  page: 1,
  page_size: 20,
})

const canRestore = computed(() => subject.can('edit_question'))

onMounted(() => {
  void loadQuestions()
  void loadPapers()
})

watch(
  () => subject.currentId,
  () => {
    questionQuery.page = 1
    paperQuery.page = 1
    void loadQuestions()
    void loadPapers()
  },
)

async function loadQuestions() {
  if (!subject.currentId) {
    questionRows.value = []
    questionTotal.value = 0
    return
  }
  questionLoading.value = true
  try {
    const result = await questionApi.listDeleted(subject.currentId, { ...questionQuery })
    questionRows.value = result.list ?? []
    questionTotal.value = result.total ?? 0
  } catch {
    questionRows.value = []
    questionTotal.value = 0
  } finally {
    questionLoading.value = false
  }
}

async function loadPapers() {
  if (!subject.currentId) {
    paperRows.value = []
    paperTotal.value = 0
    return
  }
  paperLoading.value = true
  try {
    const result = await paperApi.listDeleted(subject.currentId, { ...paperQuery })
    paperRows.value = result.list ?? []
    paperTotal.value = result.total ?? 0
  } catch {
    paperRows.value = []
    paperTotal.value = 0
  } finally {
    paperLoading.value = false
  }
}

/** 改筛选条件后回到第一页：停在第 5 页却只筛出 3 条会很困惑。 */
function searchQuestions() {
  questionQuery.page = 1
  void loadQuestions()
}

function searchPapers() {
  paperQuery.page = 1
  void loadPapers()
}

async function restoreQuestion(row: QuestionBrief) {
  try {
    await ElMessageBox.confirm('确定把这道题恢复到题库吗？', '恢复确认', {
      type: 'info',
      confirmButtonText: '恢复',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await questionApi.restore(row.id)
    ElMessage.success('已恢复')
  } catch {
    // 接口层已经弹出错误提示，这里只需要刷新列表。
  }
  await loadQuestions()
}

async function restorePaper(row: PaperBrief) {
  try {
    await ElMessageBox.confirm(`确定把试卷「${row.title}」恢复到列表吗？`, '恢复确认', {
      type: 'info',
      confirmButtonText: '恢复',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await paperApi.restore(row.id)
    ElMessage.success('已恢复')
  } catch {
    // 接口层已经弹出错误提示，这里只需要刷新列表。
  }
  await loadPapers()
}

function typeTag(type: QuestionType): 'primary' | 'success' | 'warning' | 'info' | 'danger' {
  const map: Record<QuestionType, 'primary' | 'success' | 'warning' | 'info' | 'danger'> = {
    single_choice: 'primary',
    multiple_choice: 'success',
    fill_blank: 'warning',
    true_false: 'info',
    essay: 'danger',
  }
  return map[type] ?? 'info'
}

function paperStatusTag(status: PaperBrief['status']): 'info' | 'success' | 'danger' {
  if (status === 'finalized') return 'success'
  if (status === 'archived') return 'danger'
  return 'info'
}

function formatTime(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="回收站" description="删除的题目与试卷会先放进这里，确认误删后可以恢复。" />

    <el-alert
      type="info"
      show-icon
      :closable="false"
      title="删除的题目与试卷会先放进这里，确认误删后可以恢复。"
      description="删除只是标记，数据仍在服务器上；后端目前没有彻底删除（硬删）接口，因此这里只提供恢复。"
    />

    <el-card shadow="never">
      <el-tabs v-model="tab">
        <!-- 已删除的题目 -->
        <el-tab-pane label="题目" name="questions">
          <div class="flex flex-wrap items-center gap-2">
            <el-input
              v-model="questionQuery.keyword"
              placeholder="搜索题干"
              clearable
              class="w-56"
              @keyup.enter="searchQuestions"
              @clear="searchQuestions"
            >
              <template #prefix><el-icon><Search /></el-icon></template>
            </el-input>

            <el-select v-model="questionQuery.type" placeholder="题型" clearable class="w-28" @change="searchQuestions">
              <el-option label="单选题" value="single_choice" />
              <el-option label="多选题" value="multiple_choice" />
              <el-option label="填空题" value="fill_blank" />
              <el-option label="判断题" value="true_false" />
              <el-option label="解答题" value="essay" />
            </el-select>

            <el-button @click="searchQuestions">搜索</el-button>

            <span v-if="!subject.currentId" class="ml-auto text-xs text-ink-subtle">
              请先在右上角选择学科
            </span>
          </div>

          <el-table v-loading="questionLoading" :data="questionRows" row-key="id" class="mt-3">
            <el-table-column label="题干" min-width="320">
              <template #default="{ row }">
                <RichContent
                  v-if="row.stem_doc"
                  :doc="row.stem_doc"
                  compact
                  class="stem-clamp text-sm"
                />
                <div v-else class="text-sm text-ink-subtle">{{ row.stem_text || '（无题干）' }}</div>
                <div v-if="row.source" class="mt-0.5 truncate text-xs text-ink-subtle">
                  来源：{{ row.source }}
                </div>
              </template>
            </el-table-column>

            <el-table-column label="题型" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="typeTag(row.question_type)">
                  {{ row.question_type_label }}
                </el-tag>
              </template>
            </el-table-column>

            <el-table-column label="难度" width="80">
              <template #default="{ row }">
                <DifficultyStars :level="row.difficulty" />
              </template>
            </el-table-column>

            <el-table-column label="删除前状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" effect="plain">{{ row.status_label }}</el-tag>
              </template>
            </el-table-column>

            <el-table-column label="删除时间" width="140">
              <template #default="{ row }">
                <span class="text-xs text-ink-muted">{{ formatTime(row.updated_at) }}</span>
              </template>
            </el-table-column>

            <el-table-column label="操作" width="90" fixed="right">
              <template #default="{ row }">
                <el-button
                  v-if="canRestore"
                  text
                  size="small"
                  type="primary"
                  @click="restoreQuestion(row)"
                >
                  恢复
                </el-button>
                <span v-else class="text-xs text-ink-subtle">无权限</span>
              </template>
            </el-table-column>

            <template #empty>
              <EmptyState
                :description="
                  !subject.currentId
                    ? '请先选择学科'
                    : questionQuery.keyword || questionQuery.type
                      ? '没有匹配的已删除题目'
                      : '回收站里没有题目'
                "
              />
            </template>
          </el-table>

          <div class="mt-3 flex justify-end">
            <el-pagination
              v-model:current-page="questionQuery.page"
              v-model:page-size="questionQuery.page_size"
              :total="questionTotal"
              :page-sizes="[10, 20, 50, 100]"
              layout="total, sizes, prev, pager, next, jumper"
              @current-change="loadQuestions"
              @size-change="searchQuestions"
            />
          </div>
        </el-tab-pane>

        <!-- 已删除的试卷 -->
        <el-tab-pane label="试卷" name="papers">
          <div class="flex flex-wrap items-center gap-2">
            <el-input
              v-model="paperQuery.keyword"
              placeholder="搜索试卷标题"
              clearable
              class="w-56"
              @keyup.enter="searchPapers"
              @clear="searchPapers"
            >
              <template #prefix><el-icon><Search /></el-icon></template>
            </el-input>

            <el-select v-model="paperQuery.scope" placeholder="归属" clearable class="w-28" @change="searchPapers">
              <el-option label="个人" value="personal" />
              <el-option label="共享" value="shared" />
            </el-select>

            <el-button @click="searchPapers">搜索</el-button>

            <span v-if="!subject.currentId" class="ml-auto text-xs text-ink-subtle">
              请先在右上角选择学科
            </span>
          </div>

          <el-table v-loading="paperLoading" :data="paperRows" row-key="id" class="mt-3">
            <el-table-column label="标题" min-width="240">
              <template #default="{ row }">
                <span class="text-sm">{{ row.title }}</span>
                <el-tag v-if="row.scope === 'shared'" size="small" class="ml-2" type="info">共享</el-tag>
                <div v-if="row.description" class="mt-0.5 truncate text-xs text-ink-subtle">
                  {{ row.description }}
                </div>
              </template>
            </el-table-column>

            <el-table-column label="题目数" width="90">
              <template #default="{ row }">
                <span class="text-ink-regular">{{ row.question_count }}</span>
              </template>
            </el-table-column>

            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="paperStatusTag(row.status)">{{ row.status_label }}</el-tag>
              </template>
            </el-table-column>

            <el-table-column label="修订" width="70">
              <template #default="{ row }">
                <span class="text-xs text-ink-subtle">r{{ row.revision }}</span>
              </template>
            </el-table-column>

            <el-table-column label="删除时间" width="140">
              <template #default="{ row }">
                <span class="text-xs text-ink-muted">{{ formatTime(row.updated_at) }}</span>
              </template>
            </el-table-column>

            <el-table-column label="操作" width="90" fixed="right">
              <template #default="{ row }">
                <el-button
                  v-if="canRestore"
                  text
                  size="small"
                  type="primary"
                  @click="restorePaper(row)"
                >
                  恢复
                </el-button>
                <span v-else class="text-xs text-ink-subtle">无权限</span>
              </template>
            </el-table-column>

            <template #empty>
              <EmptyState
                :description="
                  !subject.currentId
                    ? '请先选择学科'
                    : paperQuery.keyword || paperQuery.scope
                      ? '没有匹配的已删除试卷'
                      : '回收站里没有试卷'
                "
              />
            </template>
          </el-table>

          <div class="mt-3 flex justify-end">
            <el-pagination
              v-model:current-page="paperQuery.page"
              v-model:page-size="paperQuery.page_size"
              :total="paperTotal"
              :page-sizes="[10, 20, 50]"
              layout="total, sizes, prev, pager, next"
              @current-change="loadPapers"
              @size-change="searchPapers"
            />
          </div>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>
