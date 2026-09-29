<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { paperApi, type PaperBrief, type PaperFolder, type PaperScope, type PaperStatus } from '@/api/paper'
import { useSubjectStore } from '@/stores/subject'

const router = useRouter()
const subject = useSubjectStore()

const loading = ref(false)
const rows = ref<PaperBrief[]>([])
const total = ref(0)
const folders = ref<PaperFolder[]>([])

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
      paperApi.folderTree(subject.currentId).catch(() => []),
    ])
    rows.value = list.list ?? []
    total.value = list.total ?? 0
    folders.value = tree
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
      if (folder.children?.length) walk(folder.children, depth + 1)
    }
  }
  walk(folders.value, 0)
  return out
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

async function submitFolder() {
  if (!subject.currentId || !folderName.value.trim()) {
    ElMessage.warning('请输入目录名称')
    return
  }
  await paperApi.createFolder(subject.currentId, {
    name: folderName.value.trim(),
    scope: folderScope.value,
    parent_id: query.folder_id,
  })
  ElMessage.success('已创建目录')
  folderVisible.value = false
  folderName.value = ''
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

async function exportPaper(row: PaperBrief, format: 'docx' | 'latex') {
  try {
    await paperApi.exportPaper(row.id, format, row.title)
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
  <div class="flex gap-3">
    <!-- 目录侧栏 -->
    <el-card shadow="never" class="w-56 shrink-0">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">目录</span>
          <el-button text size="small" @click="folderVisible = true">
            <el-icon><Plus /></el-icon>
          </el-button>
        </div>
      </template>

      <div
        class="cursor-pointer rounded px-2 py-1.5 text-sm"
        :class="query.folder_id === null ? 'bg-blue-50 text-blue-600' : 'hover:bg-gray-50'"
        @click="pickFolder(null)"
      >
        全部试卷
      </div>

      <div
        v-for="item in flatFolders"
        :key="item.folder.id"
        class="flex cursor-pointer items-center rounded px-2 py-1.5 text-sm"
        :class="query.folder_id === item.folder.id ? 'bg-blue-50 text-blue-600' : 'hover:bg-gray-50'"
        :style="{ paddingLeft: `${8 + item.depth * 12}px` }"
        @click="pickFolder(item.folder.id)"
      >
        <el-icon class="mr-1 text-gray-400"><Folder /></el-icon>
        <span class="flex-1 truncate">{{ item.folder.name }}</span>
        <span class="text-xs text-gray-400">{{ item.folder.paper_count }}</span>
      </div>

      <el-empty v-if="flatFolders.length === 0" description="暂无目录" :image-size="48" />
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

          <el-button
            v-if="canEdit"
            type="primary"
            class="ml-auto"
            :disabled="!subject.currentId"
            @click="openCreate"
          >
            <el-icon class="mr-1"><Plus /></el-icon>新建试卷
          </el-button>
        </div>
      </el-card>

      <el-card shadow="never">
        <el-table v-loading="loading" :data="rows" row-key="id">
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
              <div v-if="row.description" class="mt-0.5 truncate text-xs text-gray-400">
                {{ row.description }}
              </div>
            </template>
          </el-table-column>

          <el-table-column label="题目数" width="90">
            <template #default="{ row }">
              <span class="text-gray-600">{{ row.question_count }}</span>
            </template>
          </el-table-column>

          <el-table-column label="总分" width="80">
            <template #default="{ row }">
              <span class="text-gray-600">{{ row.scoring_enabled ? row.total_score : '—' }}</span>
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
              <span class="text-xs text-gray-400">r{{ row.revision }}</span>
            </template>
          </el-table-column>

          <el-table-column label="更新于" width="130">
            <template #default="{ row }">
              <span class="text-xs text-gray-500">{{ formatTime(row.updated_at) }}</span>
            </template>
          </el-table-column>

          <el-table-column label="操作" width="230" fixed="right">
            <template #default="{ row }">
              <el-button
                text
                size="small"
                @click="router.push({ name: 'paper-editor', params: { id: row.id } })"
              >
                编辑
              </el-button>
              <el-button text size="small" @click="duplicate(row)">复制</el-button>
              <el-dropdown trigger="click" @command="(f: 'docx' | 'latex') => exportPaper(row, f)">
                <el-button text size="small">导出</el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="docx">导出 Word</el-dropdown-item>
                    <el-dropdown-item command="latex">导出 LaTeX 压缩包</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
              <el-button v-if="row.status === 'draft'" text size="small" type="danger" @click="removePaper(row)">
                删除
              </el-button>
            </template>
          </el-table-column>

          <template #empty>
            <el-empty :description="subject.currentId ? '还没有试卷' : '请先选择学科'" />
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
        <el-form-item v-if="query.folder_id" label="上级">
          <span class="text-sm text-gray-500">
            将建在「{{ flatFolders.find((f) => f.folder.id === query.folder_id)?.folder.name }}」下
          </span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="folderVisible = false">取消</el-button>
        <el-button type="primary" @click="submitFolder">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>
