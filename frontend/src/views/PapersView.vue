<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ApiError } from '@/api/http'
import { paperApi, type ExportContent, type PaperBrief, type PaperFolder, type PaperScope, type PaperStatus } from '@/api/paper'
import { useSubjectStore } from '@/stores/subject'
import EmptyState from '@/components/ui/EmptyState.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import PageHeader from '@/components/ui/PageHeader.vue'

const router = useRouter()
const subject = useSubjectStore()

const loading = ref(false)
const rows = ref<PaperBrief[]>([])
const total = ref(0)
const folders = ref<PaperFolder[]>([])
const collapsedFolders = ref(new Set<number>())
const exportVisible = ref(false)
const exportTarget = ref<PaperBrief | null>(null)
const exportForm = reactive<{ format: 'docx' | 'latex'; content: ExportContent }>({
  format: 'docx',
  content: 'all',
})

const query = reactive({
  keyword: '',
  scope: '' as PaperScope | '',
  status: '' as PaperStatus | '',
  folder_id: null as number | null,
  page: 1,
  page_size: 20,
})

const createVisible = ref(false)
const createForm = reactive({
  title: '',
  description: '',
  scope: 'personal' as PaperScope,
  folder_id: null as number | null,
  numbering_enabled: true,
  scoring_enabled: false,
})

const folderVisible = ref(false)
const folderName = ref('')
const folderScope = ref<PaperScope>('personal')
/** 新建目录的上级。侧栏菜单里选「新建子目录」时显式指定，否则跟随当前筛选。 */
const folderParentId = ref<number | null>(null)

/** 移动 / 重命名 / 删除的作用目标。 */
const folderTarget = ref<PaperFolder | null>(null)
const moveVisible = ref(false)
const moveParentId = ref<number | null>(null)

const canEdit = computed(() => subject.can('edit_question'))

onMounted(load)
watch(() => subject.currentId, () => {
  query.page = 1
  query.folder_id = null
  void load()
})

async function load() {
  if (!subject.currentId) return
  loading.value = true
  try {
    const [list, tree] = await Promise.all([
      paperApi.list(subject.currentId, query),
      paperApi.folderTree(subject.currentId).catch(() => [] as PaperFolder[]),
    ])
    rows.value = list.list ?? []
    total.value = list.total ?? 0
    folders.value = tree ?? []
  } catch {
    rows.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function reload() {
  query.page = 1
  void load()
}

function pickFolder(id: number | null) {
  query.folder_id = id
  reload()
}

/** 目录是两层，拍平成一维供侧栏渲染。 */
interface FlatFolder {
  folder: PaperFolder
  depth: number
}

const flatFolders = computed(() => {
  const out: FlatFolder[] = []
  const walk = (list: PaperFolder[], depth: number) => {
    for (const folder of list) {
      out.push({ folder, depth })
      if (folder.children?.length && !collapsedFolders.value.has(folder.id)) {
        walk(folder.children, depth + 1)
      }
    }
  }
  walk(folders.value, 0)
  return out
})

function toggleFolder(id: number) {
  const next = new Set(collapsedFolders.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  collapsedFolders.value = next
}

function findFolder(id: number): PaperFolder | null {
  for (const item of flatFolders.value) {
    if (item.folder.id === id) return item.folder
  }
  return null
}

/** 某个目录的整棵子树 id。 */
function subtreeIDs(folder: PaperFolder): number[] {
  const out: number[] = []
  const walk = (list: PaperFolder[]) => {
    for (const child of list) {
      out.push(child.id)
      if (child.children?.length) walk(child.children)
    }
  }
  walk(folder.children ?? [])
  return out
}

/**
 * 可选的上级目录。
 *
 * 剔掉自己与自己的整棵子树：移动到自己的后代里会让树成环，之后永远走不出来。
 * 服务端也会拦（IsDescendant），但界面上先剔掉能少一次无谓的报错。
 * 另外只列同空间的目录——空间不同的目录不能成为父子。
 */
const moveCandidates = computed(() => {
  const target = folderTarget.value
  if (!target) return []
  const banned = new Set<number>([target.id, ...subtreeIDs(target)])
  return flatFolders.value.filter(
    (item) => !banned.has(item.folder.id) && item.folder.scope === target.scope,
  )
})

function openCreate() {
  createForm.title = ''
  createForm.description = ''
  createForm.scope = 'personal'
  createForm.folder_id = query.folder_id
  createForm.numbering_enabled = true
  createForm.scoring_enabled = false
  createVisible.value = true
}

async function submitCreate() {
  if (!subject.currentId) return
  if (!createForm.title.trim()) {
    ElMessage.warning('请输入试卷标题')
    return
  }
  const paper = await paperApi.create(subject.currentId, {
    title: createForm.title.trim(),
    description: createForm.description,
    scope: createForm.scope,
    folder_id: createForm.folder_id,
    numbering_enabled: createForm.numbering_enabled,
    scoring_enabled: createForm.scoring_enabled,
  })
  ElMessage.success('已创建，正在打开编辑台')
  createVisible.value = false
  // 建完直接进编辑台：新建试卷的下一步一定是往里面加题。
  await router.push({ name: 'paper-editor', params: { id: paper.id } })
}

function openCreateFolder(parentID: number | null) {
  folderName.value = ''
  folderParentId.value = parentID
  folderVisible.value = true
}

async function submitFolder() {
  if (!subject.currentId || !folderName.value.trim()) {
    ElMessage.warning('请输入目录名称')
    return
  }
  await paperApi.createFolder(subject.currentId, {
    name: folderName.value.trim(),
    scope: folderScope.value,
    parent_id: folderParentId.value,
  })
  ElMessage.success('已创建目录')
  folderVisible.value = false
  folderName.value = ''
  await load()
}

type FolderCommand = 'child' | 'rename' | 'move' | 'delete'

function onFolderCommand(command: FolderCommand, folder: PaperFolder) {
  folderTarget.value = folder
  switch (command) {
    case 'child':
      // 新建子目录沿用父目录的空间：跨空间的父子关系服务端直接拒绝。
      folderScope.value = folder.scope
      openCreateFolder(folder.id)
      break
    case 'rename':
      void renameFolder(folder)
      break
    case 'move':
      moveParentId.value = folder.parent_id
      moveVisible.value = true
      break
    case 'delete':
      void removeFolder(folder)
      break
  }
}

async function renameFolder(folder: PaperFolder) {
  let name: string
  try {
    const { value } = await ElMessageBox.prompt('新的目录名称', '重命名目录', {
      inputValue: folder.name,
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputValidator: (v) => (v && v.trim() ? true : '目录名称不能为空'),
    })
    name = value.trim()
  } catch {
    return
  }
  if (name === folder.name) return
  try {
    await paperApi.updateFolder(folder.id, { name })
  } catch (error) {
    // 409 是"同级重名"，接口层按约定不弹提示，这里补上。
    if (error instanceof ApiError && error.isConflict) ElMessage.warning('同级下已存在同名目录')
    return
  }
  ElMessage.success('已重命名')
  await load()
}

async function submitMoveFolder() {
  const target = folderTarget.value
  if (!target) return
  const parentID = moveParentId.value
  if (parentID === target.parent_id) {
    moveVisible.value = false
    return
  }
  // 移回根目录用 clear_parent 表达：parent_id 传 null 会被 cleanParams 丢掉，
  // 结果就变成"什么都没改"。
  const payload = parentID == null ? { clear_parent: true } : { parent_id: parentID }
  try {
    await paperApi.updateFolder(target.id, payload)
  } catch (error) {
    if (error instanceof ApiError && error.isConflict) ElMessage.warning('目标位置下已存在同名目录')
    return
  }
  ElMessage.success('已移动目录')
  moveVisible.value = false
  await load()
}

/**
 * 删除目录。
 *
 * 文案刻意不承诺恢复：后端只给题目和试卷提供了恢复接口
 * （POST /questions/:id/restore、POST /papers/:id/restore），
 * 目录没有 restore 路由，回收站也列不出已删除的目录——
 * 删掉就是从界面上永久消失。说"可恢复"比不说更糟。
 */
async function removeFolder(folder: PaperFolder) {
  try {
    await ElMessageBox.confirm(
      `确定删除目录「${folder.name}」吗？\n目录需要先清空（里面不能有试卷或子目录）才能删除，且删除后无法恢复。`,
      '删除目录',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  try {
    await paperApi.deleteFolder(folder.id)
  } catch (error) {
    if (error instanceof ApiError && error.isConflict) {
      ElMessage.warning(
        error.detail
          ? `目录不为空，无法删除（${error.detail}）。请先把试卷与子目录移走。`
          : '目录不为空，无法删除。请先把试卷与子目录移走。',
      )
    }
    return
  }
  ElMessage.success('已删除目录')
  // 选中的正是被删目录时回到"全部试卷"，否则列表会停在一个筛不到东西的视图上。
  if (query.folder_id === folder.id) query.folder_id = null
  await load()
}

async function removePaper(row: PaperBrief) {
  try {
    await ElMessageBox.confirm(
      `确定删除试卷「${row.title}」吗？删除后可在回收站中恢复。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await paperApi.remove(row.id)
  ElMessage.success('已删除')
  await load()
}

async function duplicate(row: PaperBrief) {
  try {
    const { value } = await ElMessageBox.prompt('新试卷的标题', '复制试卷', {
      confirmButtonText: '复制',
      cancelButtonText: '取消',
      inputValue: `${row.title} - 副本`,
      inputValidator: (v) => (v && v.trim() ? true : '标题不能为空'),
    })
    const copy = await paperApi.duplicate(row.id, value.trim())
    ElMessage.success('已复制')
    await load()
    await router.push({ name: 'paper-editor', params: { id: copy.id } })
  } catch {
    // 用户取消
  }
}

function openExport(row: PaperBrief) {
  exportTarget.value = row
  exportVisible.value = true
}

async function confirmExport() {
  const row = exportTarget.value
  if (!row) return
  try {
    await paperApi.exportPaper(row.id, exportForm.format, row.title, exportForm.content)
    exportVisible.value = false
    ElMessage.success('已开始下载')
  } catch {
    // 错误提示由接口层处理
  }
}

function statusTag(status: PaperStatus): 'info' | 'success' | 'danger' {
  if (status === 'finalized') return 'success'
  if (status === 'archived') return 'danger'
  return 'info'
}

function formatTime(value: string): string {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="试卷" description="按目录组织试卷，进入组卷工作台后可拖拽排序、插入分页并导出 Word 或 LaTeX。" />

    <div class="flex gap-3">
    <!-- 目录侧栏 -->
    <el-card shadow="never" class="w-56 shrink-0">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">目录</span>
          <el-button
            text
            size="small"
            aria-label="新建目录"
            @click="openCreateFolder(query.folder_id)"
          >
            <el-icon><Plus /></el-icon>
          </el-button>
        </div>
      </template>

      <div
        class="cursor-pointer rounded px-2 py-1.5 text-sm"
        :class="query.folder_id === null ? 'bg-accent-soft text-accent-text' : 'hover:bg-surface-sunken'"
        @click="pickFolder(null)"
      >
        全部试卷
      </div>

      <div
        v-for="item in flatFolders"
        :key="item.folder.id"
        class="group flex cursor-pointer items-center rounded px-2 py-1.5 text-sm"
        :class="query.folder_id === item.folder.id ? 'bg-accent-soft text-accent-text' : 'hover:bg-surface-sunken'"
        :style="{ paddingLeft: `${8 + item.depth * 12}px` }"
        @click="pickFolder(item.folder.id)"
      >
        <el-icon
          v-if="item.folder.children?.length"
          class="mr-1 cursor-pointer text-ink-subtle"
          role="button"
          :aria-label="collapsedFolders.has(item.folder.id) ? '展开目录' : '折叠目录'"
          @click.stop="toggleFolder(item.folder.id)"
        >
          <ArrowRight v-if="collapsedFolders.has(item.folder.id)" />
          <ArrowDown v-else />
        </el-icon>
        <span v-else class="mr-1 w-4" />
        <el-icon class="mr-1 text-ink-subtle"><Folder /></el-icon>
        <span class="flex-1 truncate">{{ item.folder.name }}</span>
        <span class="text-xs text-ink-subtle">{{ item.folder.paper_count }}</span>
        <!-- 包一层 span 阻止冒泡：点菜单不该顺手把侧栏筛选也切过去。
             el-dropdown 自己的监听在触发元素上，外层拦冒泡不影响它展开。 -->
        <span v-if="canEdit" class="ml-1" @click.stop>
          <el-dropdown trigger="click" @command="(cmd: FolderCommand) => onFolderCommand(cmd, item.folder)">
            <el-button
              text
              size="small"
              class="!h-5 !w-5 opacity-0 transition-opacity group-hover:opacity-100"
              aria-label="目录操作"
            >
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="child">新建子目录</el-dropdown-item>
                <el-dropdown-item command="rename">重命名</el-dropdown-item>
                <el-dropdown-item command="move">移动到…</el-dropdown-item>
                <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </span>
      </div>

      <EmptyState v-if="flatFolders.length === 0" description="暂无目录" />
    </el-card>

    <div class="min-w-0 flex-1 space-y-3">
      <el-card shadow="never">
        <div class="flex flex-wrap items-center gap-2">
          <el-input
            v-model="query.keyword"
            placeholder="搜索试卷标题"
            clearable
            class="w-52"
            @keyup.enter="reload"
            @clear="reload"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>

          <el-select v-model="query.scope" placeholder="归属" clearable class="w-28" @change="reload">
            <el-option label="个人" value="personal" />
            <el-option label="共享" value="shared" />
          </el-select>

          <el-select v-model="query.status" placeholder="状态" clearable class="w-28" @change="reload">
            <el-option label="草稿" value="draft" />
            <el-option label="已定稿" value="finalized" />
            <el-option label="已归档" value="archived" />
          </el-select>

          <el-button @click="reload">搜索</el-button>

          <el-button text size="small" class="ml-auto" @click="router.push({ name: 'recycle-bin' })">
            <el-icon class="mr-1"><Delete /></el-icon>回收站
          </el-button>

          <el-button
            v-if="canEdit"
            type="primary"
            :disabled="!subject.currentId"
            @click="openCreate"
          >
            <el-icon class="mr-1"><Plus /></el-icon>新建试卷
          </el-button>
        </div>
      </el-card>

      <el-card shadow="never">
        <TableSkeleton v-if="loading" :rows="6" :cols="6" />
        <el-table v-else :data="rows" row-key="id">
          <el-table-column label="标题" min-width="240">
            <template #default="{ row }">
              <el-link
                type="primary"
                :underline="false"
                @click="router.push({ name: 'paper-editor', params: { id: row.id } })"
              >
                {{ row.title }}
              </el-link>
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

          <el-table-column label="总分" width="80">
            <template #default="{ row }">
              <span class="text-ink-regular">{{ row.scoring_enabled ? row.total_score : '—' }}</span>
            </template>
          </el-table-column>

          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="statusTag(row.status)">{{ row.status_label }}</el-tag>
              <el-tag
                v-if="row.finalized_version"
                size="small"
                type="success"
                effect="plain"
                class="ml-1"
              >
                v{{ row.finalized_version }}
              </el-tag>
            </template>
          </el-table-column>

          <el-table-column label="修订" width="70">
            <template #default="{ row }">
              <span class="num text-xs text-ink-subtle">r{{ row.revision }}</span>
            </template>
          </el-table-column>

          <el-table-column label="更新于" width="130">
            <template #default="{ row }">
              <span class="text-xs text-ink-muted">{{ formatTime(row.updated_at) }}</span>
            </template>
          </el-table-column>

          <el-table-column label="操作" width="200" fixed="right">
            <template #default="{ row }">
              <div class="flex items-center gap-1 whitespace-nowrap">
                <el-button
                  text
                  size="small"
                  @click="router.push({ name: 'paper-editor', params: { id: row.id } })"
                >
                  编辑
                </el-button>
                <el-button text size="small" @click="duplicate(row)">复制</el-button>
                <el-button text size="small" @click="openExport(row)">导出</el-button>
                <el-button
                  v-if="row.status === 'draft'"
                  text
                  size="small"
                  type="danger"
                  @click="removePaper(row)"
                >
                  删除
                </el-button>
              </div>
            </template>
          </el-table-column>

          <template #empty>
            <EmptyState
              :description="subject.currentId ? '还没有试卷' : '请先选择学科'"
              :hint="subject.currentId ? '新建一份试卷后进入组卷工作台，从题库或试题篮选题排版。' : ''"
              :action-label="subject.currentId ? '新建试卷' : undefined"
              @action="openCreate"
            />
          </template>
        </el-table>

        <div class="mt-3 flex justify-end">
          <el-pagination
            v-model:current-page="query.page"
            v-model:page-size="query.page_size"
            :total="total"
            :page-sizes="[10, 20, 50]"
            layout="total, sizes, prev, pager, next"
            @current-change="load"
            @size-change="reload"
          />
        </div>
      </el-card>
    </div>

    <!-- 新建试卷 -->
    <el-dialog v-model="createVisible" title="新建试卷" width="480px">
      <el-form label-width="80px">
        <el-form-item label="标题" required>
          <el-input v-model="createForm.title" placeholder="如：高一数学第一次月考" maxlength="200" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="createForm.description" type="textarea" :rows="2" placeholder="选填" />
        </el-form-item>
        <el-form-item label="归属">
          <el-radio-group v-model="createForm.scope">
            <el-radio value="personal">个人</el-radio>
            <el-radio value="shared">共享</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="目录">
          <el-select v-model="createForm.folder_id" clearable placeholder="不放入目录" class="w-full">
            <el-option v-for="item in flatFolders" :key="item.folder.id" :label="item.folder.name" :value="item.folder.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="选项">
          <div class="space-y-1">
            <el-checkbox v-model="createForm.numbering_enabled">自动编号题号</el-checkbox>
            <el-checkbox v-model="createForm.scoring_enabled">启用分值统计</el-checkbox>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" @click="submitCreate">创建并编辑</el-button>
      </template>
    </el-dialog>

    <!-- 新建目录 -->
    <el-dialog v-model="folderVisible" title="新建目录" width="380px">
      <el-form label-width="70px">
        <el-form-item label="名称" required>
          <el-input v-model="folderName" placeholder="如：高二上学期" maxlength="128" @keyup.enter="submitFolder" />
        </el-form-item>
        <el-form-item label="归属">
          <el-radio-group v-model="folderScope">
            <el-radio value="personal">个人</el-radio>
            <el-radio value="shared">共享</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="folderParentId" label="上级">
          <span class="text-sm text-ink-muted">
            将建在「{{ findFolder(folderParentId)?.name ?? '未知目录' }}」下
          </span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="folderVisible = false">取消</el-button>
        <el-button type="primary" @click="submitFolder">创建</el-button>
      </template>
    </el-dialog>

    <!-- 移动目录 -->
    <el-dialog v-model="moveVisible" title="移动目录" width="400px">
      <div v-if="folderTarget" class="text-sm text-ink-muted">
        把「{{ folderTarget.name }}」移动到：
      </div>
      <el-select v-model="moveParentId" class="mt-2 w-full" placeholder="根目录">
        <el-option label="根目录（不放入任何目录）" :value="null" />
        <el-option
          v-for="item in moveCandidates"
          :key="item.folder.id"
          :label="'　'.repeat(item.depth) + item.folder.name"
          :value="item.folder.id"
        />
      </el-select>
      <p class="mt-2 text-xs text-ink-subtle">
        只能移到同一空间（个人 / 共享）下的其它目录；目录里的试卷会跟着一起搬走。
      </p>
      <template #footer>
        <el-button @click="moveVisible = false">取消</el-button>
        <el-button type="primary" @click="submitMoveFolder">移动</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="exportVisible" title="导出设置" width="480px" append-to-body>
      <el-form label-width="90px">
        <el-form-item label="格式">
          <el-radio-group v-model="exportForm.format">
            <el-radio-button value="docx">Word</el-radio-button>
            <el-radio-button value="latex">LaTeX</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="内容">
          <el-radio-group v-model="exportForm.content">
            <el-radio-button value="all">题目 + 答案解析</el-radio-button>
            <el-radio-button value="questions">仅题目</el-radio-button>
            <el-radio-button value="answers">仅答案解析</el-radio-button>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="exportVisible = false">取消</el-button>
        <el-button type="primary" @click="confirmExport">下载</el-button>
      </template>
    </el-dialog>
    </div>
  </div>
</template>
