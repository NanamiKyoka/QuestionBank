<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { paperApi, type PaperQuestionProjection, type PaperVersion, type PaperSnapshot, type SnapshotNode } from '@/api/paper'
import RichContent from '@/components/RichContent.vue'

/**
 * 定稿版本只读预览。
 *
 * 只消费 `GET /papers/:id/versions/:n` 返回的**冻结快照**：
 * 快照里连题目内容一起冻结了，所以这里不会、也不该再去查题库——
 * 之后再怎么改题库，这个页面看到的内容都不会变，那正是"定稿"的意义。
 * 页面上没有任何编辑入口：改这份内容要走编辑台，那会产生新的草稿与新版本。
 *
 * 渲染顺序与取号规则刻意与导出器
 * （backend/internal/export/assemble.go 的 Assemble）保持一致：
 * 预览和导出的 docx/tex 长得不一样时，优先怀疑这里，而不是怀疑导出。
 */
const route = useRoute()
const router = useRouter()

const paperId = computed(() => Number(route.params.id))
const versionNo = computed(() => Number(route.params.versionNo))

const loading = ref(false)
const exporting = ref(false)
const notFound = ref(false)
const version = ref<PaperVersion | null>(null)
const snapshot = ref<PaperSnapshot | null>(null)

async function load() {
  if (!Number.isFinite(paperId.value) || !Number.isFinite(versionNo.value)) {
    notFound.value = true
    return
  }
  loading.value = true
  notFound.value = false
  try {
    const detail = await paperApi.getVersion(paperId.value, versionNo.value)
    version.value = detail.version
    snapshot.value = detail.snapshot
  } catch {
    // 越权与不存在都由后端收敛成 404，界面统一当作"看不到"。
    notFound.value = true
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch([paperId, versionNo], load)

function backToEditor() {
  void router.push({ name: 'paper-editor', params: { id: paperId.value } })
}

function formatTime(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString('zh-CN')
}

async function doExport(format: 'docx' | 'latex') {
  if (!version.value) return
  exporting.value = true
  try {
    await paperApi.exportVersion(paperId.value, versionNo.value, format, version.value.title)
  } finally {
    exporting.value = false
  }
}

// ---- 树还原 ----

const roots = computed<SnapshotNode[]>(() =>
  (snapshot.value?.nodes ?? [])
    .filter((n) => n.parent_id === null && n.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position),
)

/** 逐题跟随的解析条目（scope=before），键是题目块节点 id。 */
const anchoredItems = computed(() => {
  const map = new Map<string, SnapshotNode[]>()
  for (const node of snapshot.value?.nodes ?? []) {
    if (node.node_kind !== 'reference' || !node.anchor_before_node_id) continue
    const list = map.get(node.anchor_before_node_id)
    if (list) list.push(node)
    else map.set(node.anchor_before_node_id, [node])
  }
  return map
})

function moduleChildren(moduleID: string): SnapshotNode[] {
  return (snapshot.value?.nodes ?? [])
    .filter((n) => n.parent_id === moduleID)
    .slice()
    .sort((a, b) => a.position - b.position)
}

/** 解析模块是不是"每题之后"模式：全部条目都带锚点。 */
function isAnchoredModule(moduleID: string): boolean {
  const items = moduleChildren(moduleID)
  return items.length > 0 && items.every((i) => i.anchor_before_node_id)
}

// ---- 题号 ----

/**
 * 题目块的位置序号。
 *
 * 与导出器的 counter 同义：没写 props.number 的题目按出现顺序取这个值。
 * 预先算成映射而不是在模板里维护一个自增变量——模板里的自增变量在
 * 条件渲染下会算错（题目被跳过时计数器照样前进）。
 */
const positionNumbers = computed(() => {
  const map = new Map<string, number>()
  let n = 0
  for (const node of roots.value) {
    if (node.node_type !== 'question') continue
    n += 1
    map.set(node.id, n)
  }
  return map
})

/**
 * 题号标签。
 *
 * 与导出器同一个优先级：节点上的 props.number 优先（可能是 "1.1" 这类
 * 十进制编号），没写才用位置计数。
 */
function numberLabel(node: SnapshotNode): string {
  const raw = propsOf<{ number?: string }>(node).number
  if (typeof raw === 'string' && raw.trim()) return raw.trim()
  return String(positionNumbers.value.get(node.id) ?? 0)
}

function scoreOf(node: SnapshotNode): number | null {
  const raw = propsOf<{ score?: number }>(node).score
  return typeof raw === 'number' && Number.isFinite(raw) && raw > 0 ? raw : null
}

function propsOf<T>(node: SnapshotNode): T {
  return (node.props ?? {}) as T
}

function headingLevel(node: SnapshotNode): number {
  const level = propsOf<{ level?: number }>(node).level
  return typeof level === 'number' && level >= 1 && level <= 3 ? level : 1
}

function answerLines(node: SnapshotNode): number {
  const lines = propsOf<{ lines?: number }>(node).lines
  return typeof lines === 'number' && lines > 0 ? Math.min(Math.round(lines), 50) : 4
}

function isLined(node: SnapshotNode): boolean {
  return propsOf<{ style?: string }>(node).style === 'lined'
}

// ---- 题目内容 ----

function questionOf(node: SnapshotNode): PaperQuestionProjection | null {
  const ref = node.content as { question_id?: number } | null
  const id = ref?.question_id
  if (!id) return null
  return snapshot.value?.questions[String(id)] ?? null
}

function hasContent(doc: unknown): boolean {
  if (!doc || typeof doc !== 'object') return false
  const root = doc as { content?: unknown[] }
  return Array.isArray(root.content) && root.content.length > 0
}

const display = computed(() => snapshot.value?.question_display)

/** 答案的纯文本形式，选择/判断/填空题在这里就结束了。 */
function answerTextOf(q: PaperQuestionProjection): string {
  switch (q.answer.kind) {
    case 'single':
    case 'multiple':
      return (q.answer.correct ?? [])
        .map((id) => q.options.find((o) => o.id === id)?.label ?? id)
        .join('、')
    case 'boolean':
      return q.answer.bool_answer == null ? '' : q.answer.bool_answer ? '正确' : '错误'
    case 'blanks':
      return (q.answer.blanks ?? [])
        .map((b) => (b.accepts ?? []).map((g) => g.map(() => '').join('')).join(' 或 '))
        .join('；')
    default:
      // 'reference'（解答题）的答案本身就是一段富文本，由模板单独渲染；
      // 'pending'（待补充答案）没有可渲染的内容，留空才是如实的。
      return ''
  }
}

/** 解析条目对应的题目。它自己不带 question_id，要靠 source_question_node_id 反查。 */
function sourceQuestionOf(item: SnapshotNode): PaperQuestionProjection | null {
  const sourceID = item.source_question_node_id
  if (!sourceID) return null
  const node = (snapshot.value?.nodes ?? []).find((n) => n.id === sourceID)
  return node ? questionOf(node) : null
}

/** 条目的答案纯文本。取不到题目时返回空串：少一段总好过显示一段错的。 */
function answerOf(item: SnapshotNode): string {
  const q = sourceQuestionOf(item)
  return q ? answerTextOf(q) : ''
}

/** 条目的参考答案（解答题）：答案本身就是一段富文本。 */
function answerDocOf(item: SnapshotNode): unknown {
  const q = sourceQuestionOf(item)
  return q?.answer.kind === 'reference' ? q.answer.reference : null
}

function analysisOf(item: SnapshotNode): unknown {
  return sourceQuestionOf(item)?.analysis ?? null
}

function itemNumberOf(item: SnapshotNode): string {
  // 解析条目自己 props.number 上记的是服务端按位置算的整数，
  // 对不上 "1.1" 这种编号，所以优先取它所指向题目块上的题号。
  const sourceID = item.source_question_node_id
  if (sourceID) {
    const node = (snapshot.value?.nodes ?? []).find((n) => n.id === sourceID)
    if (node) {
      const raw = propsOf<{ number?: string }>(node).number
      if (typeof raw === 'string' && raw.trim()) return raw.trim()
    }
  }
  const n = propsOf<{ number?: number }>(item).number
  return typeof n === 'number' && n > 0 ? String(n) : ''
}
</script>

<template>
  <div v-loading="loading" class="space-y-3">
    <!-- 顶栏 -->
    <el-card shadow="never">
      <div class="flex flex-wrap items-center gap-2">
        <el-button text @click="backToEditor">
          <el-icon><ArrowLeft /></el-icon>
        </el-button>

        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="truncate text-base font-semibold">{{ version?.title ?? '版本预览' }}</span>
            <el-tag v-if="version" size="small" effect="plain">v{{ version.version_no }}</el-tag>
            <el-tag v-if="version" size="small" type="info">只读</el-tag>
          </div>
          <div v-if="version" class="text-xs text-gray-400">
            定稿于 {{ formatTime(version.created_at) }} · 定稿人 #{{ version.created_by }}
            <template v-if="snapshot"> · 源修订 r{{ snapshot.source_revision }}</template>
          </div>
        </div>

        <el-dropdown :disabled="!version || exporting" @command="(f: 'docx' | 'latex') => doExport(f)">
          <el-button :loading="exporting">导出<el-icon class="ml-1"><ArrowDown /></el-icon></el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="docx">Word (.docx)</el-dropdown-item>
              <el-dropdown-item command="latex">LaTeX 压缩包 (.zip)</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-button @click="backToEditor">返回编辑台</el-button>
      </div>
    </el-card>

    <el-alert v-if="notFound" type="error" :closable="false" show-icon title="无法查看该版本">
      版本不存在，或你没有访问这份试卷的权限。
    </el-alert>

    <template v-else-if="snapshot">
      <el-alert type="info" :closable="false" show-icon>
        <template #title>
          以下是定稿时刻冻结的快照：题目内容一并冻结在版本里，之后修改题库不会影响这里的内容。
          草稿当前的样子请回到编辑台查看。
        </template>
      </el-alert>

      <el-card v-if="version?.note" shadow="never">
        <span class="text-xs text-gray-400">版本说明：</span>{{ version.note }}
      </el-card>

      <el-card shadow="never">
        <div class="mx-auto max-w-3xl py-4">
          <!-- 试卷标题：与导出器一致，标题始终输出 -->
          <h1 class="text-center text-xl font-bold">{{ snapshot.title }}</h1>
          <p v-if="snapshot.scoring_enabled && snapshot.total_score > 0" class="mt-1 text-center text-sm text-gray-500">
            （满分 {{ snapshot.total_score }} 分）
          </p>

          <div class="mt-6 space-y-3">
            <template v-for="node in roots" :key="node.id">
              <!-- 说明文字 -->
              <RichContent
                v-if="node.node_type === 'rich_text'"
                :doc="node.content"
                :compact="true"
              />

              <!-- 标题 -->
              <div
                v-else-if="node.node_type === 'heading'"
                :class="[
                  'font-semibold',
                  headingLevel(node) === 1 ? 'text-lg' : headingLevel(node) === 2 ? 'text-base' : 'text-sm',
                ]"
              >
                <RichContent :doc="node.content" :compact="true" />
              </div>

              <!-- 分页符 -->
              <div v-else-if="node.node_type === 'page_break'" class="flex items-center gap-2 text-xs text-gray-300">
                <div class="h-px flex-1 bg-dashed bg-gray-300" />分页符
                <div class="h-px flex-1 bg-gray-300" />
              </div>

              <!-- 答题区 -->
              <div v-else-if="node.node_type === 'answer_space'" class="rounded border border-dashed border-gray-300 px-2 py-1">
                <template v-if="isLined(node)">
                  <div
                    v-for="line in answerLines(node)"
                    :key="line"
                    class="h-6 border-b border-dashed border-gray-300"
                  />
                </template>
                <div v-else :style="{ height: `${answerLines(node) * 24}px` }" />
              </div>

              <!-- 题目 -->
              <div v-else-if="node.node_type === 'question'">
                <div class="flex items-baseline gap-1 text-sm font-semibold">
                  <template v-if="snapshot.numbering_enabled">{{ numberLabel(node) }}．</template>
                  <template v-if="snapshot.scoring_enabled && scoreOf(node) !== null">
                    <span class="font-normal text-gray-500">（{{ scoreOf(node) }} 分）</span>
                  </template>
                </div>

                <template v-if="questionOf(node)">
                  <RichContent :doc="questionOf(node)?.stem" class="mt-0.5" />
                  <div v-if="questionOf(node)?.options.length" class="mt-1 space-y-0.5">
                    <div
                      v-for="option in questionOf(node)?.options"
                      :key="option.id"
                      class="flex gap-1 text-sm"
                    >
                      <span class="font-medium">{{ option.label }}．</span>
                      <RichContent :doc="option.content" class="flex-1" />
                    </div>
                  </div>

                  <div v-if="display?.show_analysis && hasContent(questionOf(node)?.analysis)" class="mt-1 text-sm">
                    <span class="font-semibold">【解析】</span>
                    <RichContent :doc="questionOf(node)?.analysis" :compact="true" />
                  </div>
                  <div v-if="display?.show_solution && hasContent(questionOf(node)?.solution)" class="mt-1 text-sm">
                    <span class="font-semibold">【思路】</span>
                    <RichContent :doc="questionOf(node)?.solution" :compact="true" />
                  </div>
                  <div v-if="display?.show_summary && hasContent(questionOf(node)?.summary)" class="mt-1 text-sm">
                    <span class="font-semibold">【小结】</span>
                    <RichContent :doc="questionOf(node)?.summary" :compact="true" />
                  </div>
                </template>
                <div v-else class="text-sm text-gray-400">（题目内容在定稿时未能读取）</div>

                <!-- scope=before：解析紧跟本题 -->
                <div v-for="item in anchoredItems.get(node.id) ?? []" :key="item.id" class="mt-1 text-sm">
                  <template v-if="itemNumberOf(item)">{{ itemNumberOf(item) }}．</template>
                  <span class="font-semibold">【答案】</span>
                  <span v-if="answerOf(item)">{{ answerOf(item) }}</span>
                  <RichContent v-if="answerDocOf(item)" :doc="answerDocOf(item)" :compact="true" />
                  <template v-if="hasContent(analysisOf(item))">
                    <div><span class="font-semibold">【解析】</span></div>
                    <RichContent :doc="analysisOf(item)" :compact="true" />
                  </template>
                </div>
              </div>

              <!-- 解析模块：scope=all 时的整卷汇总 -->
              <div v-else-if="node.node_type === 'question_details'">
                <template v-if="!isAnchoredModule(node.id)">
                  <h2 class="text-center text-base font-bold">参考答案与解析</h2>
                  <div v-for="item in moduleChildren(node.id)" :key="item.id" class="mt-1 text-sm">
                    <template v-if="itemNumberOf(item)">{{ itemNumberOf(item) }}．</template>
                    <span class="font-semibold">【答案】</span>
                    <span v-if="answerOf(item)">{{ answerOf(item) }}</span>
                    <RichContent v-if="answerDocOf(item)" :doc="answerDocOf(item)" :compact="true" />
                    <template v-if="hasContent(analysisOf(item))">
                      <div><span class="font-semibold">【解析】</span></div>
                      <RichContent :doc="analysisOf(item)" :compact="true" />
                    </template>
                  </div>
                </template>
              </div>
            </template>
          </div>

          <el-empty
            v-if="roots.length === 0"
            description="这个版本里没有可显示的内容"
            :image-size="72"
          />
        </div>
      </el-card>
    </template>
  </div>
</template>
