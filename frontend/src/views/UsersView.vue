<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { subjectApi, userApi } from '@/api'
import type { Subject, SubjectRole, User } from '@/api/types'
import PageHeader from '@/components/ui/PageHeader.vue'
import AdminImportDialog from '@/components/AdminImportDialog.vue'
import FilterBar from '@/components/ui/FilterBar.vue'
import TableSkeleton from '@/components/ui/TableSkeleton.vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

const loading = ref(false)
const rows = ref<User[]>([])
const total = ref(0)
const subjects = ref<Subject[]>([])

const query = reactive({ keyword: '', page: 1, page_size: 20 })

const dialogVisible = ref(false)
const importVisible = ref(false)
const form = reactive({
  username: '',
  password: '',
  nickname: '',
  email: '',
  is_superuser: false,
  subject_id: null as number | null,
  subject_role: 'editor' as SubjectRole,
})

const roleOptions: { value: SubjectRole; label: string }[] = [
  { value: 'viewer', label: '只读' },
  { value: 'editor', label: '编辑' },
  { value: 'manager', label: '负责人' },
]

onMounted(async () => {
  await Promise.all([load(), loadSubjects()])
})

async function loadSubjects() {
  try {
    subjects.value = await subjectApi.list()
  } catch {
    subjects.value = []
  }
}

async function load() {
  loading.value = true
  try {
    const result = await userApi.list(query)
    rows.value = result.list ?? []
    total.value = result.total ?? 0
  } finally {
    loading.value = false
  }
}

function reload() {
  query.page = 1
  void load()
}

function openCreate() {
  form.username = ''
  form.password = ''
  form.nickname = ''
  form.email = ''
  form.is_superuser = false
  form.subject_id = null
  form.subject_role = 'editor'
  dialogVisible.value = true
}

async function onSubmit() {
  if (!form.username.trim() || !form.password) {
    ElMessage.warning('用户名与密码必填')
    return
  }
  if (form.password.length < 8) {
    ElMessage.warning('密码至少 8 位')
    return
  }
  await userApi.create({
    username: form.username.trim(),
    password: form.password,
    nickname: form.nickname.trim(),
    email: form.email.trim(),
    is_superuser: form.is_superuser,
    // 只有勾了具体学科才带这两个字段，否则用户就是"不属于任何学科"的状态。
    ...(form.subject_id
      ? { subject_id: form.subject_id, subject_role: form.subject_role }
      : {}),
  })
  ElMessage.success('已创建')
  dialogVisible.value = false
  await load()
}

async function toggleActive(row: User) {
  await userApi.update(row.id, { is_active: !row.is_active })
  ElMessage.success(row.is_active ? '已禁用' : '已启用')
  await load()
}

async function resetPassword(row: User) {
  try {
    const { value } = await ElMessageBox.prompt(
      `为「${row.username}」设置新密码（至少 8 位）`,
      '重置密码',
      {
        confirmButtonText: '重置',
        cancelButtonText: '取消',
        inputType: 'password',
        inputValidator: (v) => (v && v.length >= 8 ? true : '密码至少 8 位'),
      },
    )
    await userApi.resetPassword(row.id, value)
    ElMessage.success('密码已重置')
  } catch {
    // 用户取消
  }
}

async function remove(row: User) {
  try {
    await ElMessageBox.confirm(
      `确定删除用户「${row.username}」吗？其创建的题目会保留。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await userApi.remove(row.id)
  ElMessage.success('已删除')
  await load()
}

function roleLabel(row: User): string {
  if (row.is_superuser) return '全局管理员'
  return '普通用户'
}
</script>

<template>
  <div class="space-y-3">
    <PageHeader title="用户管理" description="创建账号并分配学科角色。角色决定该用户在本学科能看到什么、能改什么。">
      <template #actions>
        <el-button @click="importVisible = true">批量导入</el-button>
        <el-button type="primary" @click="openCreate">
          <el-icon class="mr-1"><Plus /></el-icon>新建用户
        </el-button>
      </template>
    </PageHeader>

    <el-card shadow="never">
      <template #header>
        <FilterBar
          v-model="query.keyword"
          placeholder="搜索用户名或昵称"
          :dirty="!!query.keyword"
          @search="reload"
          @reset="((query.keyword = ''), reload())"
        />
      </template>

      <TableSkeleton v-if="loading" :rows="6" :cols="6" />
      <el-table v-else :data="rows" row-key="id">
        <el-table-column label="用户" min-width="200">
          <template #default="{ row }">
            <div class="flex items-center gap-2">
              <el-avatar :size="28">{{ (row.nickname || row.username).slice(0, 1) }}</el-avatar>
              <div>
                <div class="text-sm">{{ row.nickname || row.username }}</div>
                <div class="text-xs text-ink-subtle">{{ row.username }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="email" label="邮箱" min-width="160" show-overflow-tooltip />
        <el-table-column label="角色" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="row.is_superuser ? 'danger' : 'info'">
              {{ roleLabel(row) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.is_active ? 'success' : 'info'">
              {{ row.is_active ? '正常' : '已禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <!-- 不给自己禁用/删除的入口：容易把自己锁在系统外面。 -->
            <template v-if="row.id !== auth.user?.id">
              <el-button text size="small" @click="toggleActive(row)">
                {{ row.is_active ? '禁用' : '启用' }}
              </el-button>
              <el-button text size="small" @click="resetPassword(row)">重置密码</el-button>
              <el-button text size="small" type="danger" @click="remove(row)">删除</el-button>
            </template>
            <span v-else class="text-xs text-ink-subtle">当前账号</span>
          </template>
        </el-table-column>
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

    <AdminImportDialog v-model="importVisible" kind="users" @imported="load" />

    <el-dialog v-model="dialogVisible" title="新建用户" width="500px">
      <el-form label-width="90px">
        <el-form-item label="用户名" required>
          <el-input v-model="form.username" placeholder="字母或数字" maxlength="64" />
        </el-form-item>
        <el-form-item label="密码" required>
          <el-input v-model="form.password" type="password" show-password placeholder="至少 8 位" />
        </el-form-item>
        <el-form-item label="显示名称">
          <el-input v-model="form.nickname" maxlength="64" placeholder="选填" />
        </el-form-item>
        <el-form-item label="邮箱">
          <el-input v-model="form.email" maxlength="128" placeholder="选填" />
        </el-form-item>

        <el-divider content-position="left">
          <span class="text-xs text-ink-subtle">学科权限（可选）</span>
        </el-divider>

        <el-form-item label="加入学科">
          <el-select v-model="form.subject_id" clearable placeholder="不加入任何学科" class="w-full">
            <el-option v-for="s in subjects" :key="s.id" :label="s.name" :value="s.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="form.subject_id" label="学科角色">
          <el-select v-model="form.subject_role" class="w-full">
            <el-option v-for="role in roleOptions" :key="role.value" :label="role.label" :value="role.value" />
          </el-select>
        </el-form-item>

        <el-form-item label="全局管理员">
          <el-switch v-model="form.is_superuser" />
          <span class="ml-2 text-xs text-ink-subtle">拥有全部学科与系统设置的权限</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="onSubmit">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>
