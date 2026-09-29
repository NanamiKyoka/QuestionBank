<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { subjectApi, userApi } from '@/api'
import type { Subject, SubjectMember, SubjectRole } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { useSubjectStore } from '@/stores/subject'

const auth = useAuthStore()
const subjectStore = useSubjectStore()

const loading = ref(false)
const rows = ref<Subject[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const form = reactive({ name: '', code: '', description: '', review_threshold: 1 })

const memberVisible = ref(false)
const memberSubject = ref<Subject | null>(null)
const members = ref<SubjectMember[]>([])
const memberLoading = ref(false)
const addMemberId = ref<number | null>(null)
const addMemberRole = ref<SubjectRole>('viewer')
const candidateUsers = ref<{ id: number; username: string; nickname: string }[]>([])

const roleOptions: { value: SubjectRole; label: string; hint: string }[] = [
  { value: 'viewer', label: '只读', hint: '可以查看题目与试卷' },
  { value: 'editor', label: '编辑', hint: '可以录入与修改题目' },
  { value: 'manager', label: '负责人', hint: '可以审核题目、管理知识点与成员' },
]

onMounted(load)

async function load() {
  loading.value = true
  try {
    rows.value = await subjectApi.list()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  form.name = ''
  form.code = ''
  form.description = ''
  form.review_threshold = 1
  dialogVisible.value = true
}

function openEdit(row: Subject) {
  editingId.value = row.id
  form.name = row.name
  form.code = row.code
  form.description = row.description
  form.review_threshold = row.review_threshold
  dialogVisible.value = true
}

async function onSubmit() {
  if (!form.name.trim()) {
    ElMessage.warning('学科名称不能为空')
    return
  }
  if (editingId.value) {
    await subjectApi.update(editingId.value, {
      name: form.name.trim(),
      code: form.code.trim(),
      description: form.description,
      review_threshold: form.review_threshold,
    })
    ElMessage.success('已保存')
  } else {
    await subjectApi.create({
      name: form.name.trim(),
      code: form.code.trim(),
      description: form.description,
      review_threshold: form.review_threshold,
    })
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  // 学科列表变了，全局的学科上下文也要跟着刷新，否则顶栏下拉里看不到新学科。
  await Promise.all([load(), subjectStore.load()])
}

async function remove(row: Subject) {
  try {
    await ElMessageBox.confirm(
      `确定删除学科「${row.name}」吗？其下的题目、知识点与试卷都会被一并删除，且不可恢复。`,
      '危险操作',
      { type: 'error', confirmButtonText: '确认删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await subjectApi.remove(row.id)
  ElMessage.success('已删除')
  await Promise.all([load(), subjectStore.load()])
}

// ---- 成员管理 ----

async function openMembers(row: Subject) {
  memberSubject.value = row
  memberVisible.value = true
  await loadMembers()
  await loadCandidates()
}

async function loadMembers() {
  if (!memberSubject.value) return
  memberLoading.value = true
  try {
    members.value = await subjectApi.members(memberSubject.value.id)
  } finally {
    memberLoading.value = false
  }
}

async function loadCandidates() {
  try {
    // 只取前 200 个用户做候选：单机部署下用户量很小，
    // 真要支撑大团队时应换成后端搜索接口。
    const result = await userApi.list({ page: 1, page_size: 200 })
    const existing = new Set(members.value.map((m) => m.user_id))
    candidateUsers.value = (result.list ?? []).filter((u) => !existing.has(u.id))
  } catch {
    candidateUsers.value = []
  }
}

async function addMember() {
  if (!memberSubject.value || !addMemberId.value) {
    ElMessage.warning('请选择用户')
    return
  }
  await subjectApi.upsertMember(memberSubject.value.id, addMemberId.value, addMemberRole.value)
  ElMessage.success('已添加')
  addMemberId.value = null
  await Promise.all([loadMembers(), loadCandidates()])
}

async function changeRole(member: SubjectMember, role: SubjectRole) {
  if (!memberSubject.value) return
  await subjectApi.upsertMember(memberSubject.value.id, member.user_id, role)
  ElMessage.success('已更新角色')
  await loadMembers()
}

async function removeMember(member: SubjectMember) {
  if (!memberSubject.value) return
  try {
    await ElMessageBox.confirm(`确定将「${member.username}」移出本学科吗？`, '移出成员', {
      type: 'warning',
      confirmButtonText: '移出',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  await subjectApi.removeMember(memberSubject.value.id, member.user_id)
  ElMessage.success('已移出')
  await Promise.all([loadMembers(), loadCandidates()])
}

function hintOf(role: SubjectRole): string {
  return roleOptions.find((r) => r.value === role)?.hint ?? ''
}
// hintOf 供后续在角色下拉里显示说明，暂时保留在模板外避免未使用告警。
void hintOf
</script>

<template>
  <div class="space-y-3">
    <el-card shadow="never">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">全部学科</span>
          <el-button type="primary" size="small" @click="openCreate">
            <el-icon class="mr-1"><Plus /></el-icon>新建学科
          </el-button>
        </div>
      </template>

      <el-table v-loading="loading" :data="rows" row-key="id">
        <el-table-column prop="name" label="名称" min-width="160">
          <template #default="{ row }">
            <span class="font-medium">{{ row.name }}</span>
            <el-tag v-if="row.id === subjectStore.currentId" size="small" class="ml-2">当前</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="code" label="代号" width="120" />
        <el-table-column prop="description" label="说明" min-width="200" show-overflow-tooltip />
        <el-table-column label="审核门槛" width="100">
          <template #default="{ row }">
            <span class="text-gray-500">{{ row.review_threshold }} 人</span>
          </template>
        </el-table-column>
        <el-table-column label="我的角色" width="100">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ row.role_label }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button text size="small" @click="openMembers(row)">成员</el-button>
            <el-button v-if="auth.isSuperuser" text size="small" @click="openEdit(row)">编辑</el-button>
            <el-button v-if="auth.isSuperuser" text size="small" type="danger" @click="remove(row)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑学科' : '新建学科'" width="480px">
      <el-form label-width="90px">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如：数学" maxlength="50" />
        </el-form-item>
        <el-form-item label="代号">
          <el-input v-model="form.code" placeholder="选填，如 math" maxlength="32" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" type="textarea" :rows="2" placeholder="选填" />
        </el-form-item>
        <el-form-item label="审核门槛">
          <el-input-number v-model="form.review_threshold" :min="1" :max="10" />
          <div class="ml-2 text-xs text-gray-400">需要几位负责人通过才算审核通过</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="onSubmit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="memberVisible" :title="`成员管理 · ${memberSubject?.name ?? ''}`" size="560px">
      <div class="mb-3 flex items-center gap-2">
        <el-select v-model="addMemberId" filterable placeholder="选择用户" class="flex-1">
          <el-option
            v-for="user in candidateUsers"
            :key="user.id"
            :label="`${user.nickname || user.username}（${user.username}）`"
            :value="user.id"
          />
        </el-select>
        <el-select v-model="addMemberRole" class="w-28">
          <el-option v-for="role in roleOptions" :key="role.value" :label="role.label" :value="role.value" />
        </el-select>
        <el-button type="primary" @click="addMember">添加</el-button>
      </div>

      <el-alert type="info" :closable="false" class="mb-3">
        <div v-for="role in roleOptions" :key="role.value" class="text-xs">
          <b>{{ role.label }}</b>：{{ role.hint }}
        </div>
      </el-alert>

      <el-table v-loading="memberLoading" :data="members" row-key="user_id">
        <el-table-column label="成员" min-width="160">
          <template #default="{ row }">
            <div class="flex items-center gap-2">
              <el-avatar :size="24">{{ (row.nickname || row.username).slice(0, 1) }}</el-avatar>
              <div>
                <div class="text-sm">{{ row.nickname || row.username }}</div>
                <div class="text-xs text-gray-400">{{ row.username }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="角色" width="130">
          <template #default="{ row }">
            <el-select
              :model-value="row.role"
              size="small"
              @change="(val: SubjectRole) => changeRole(row, val)"
            >
              <el-option
                v-for="role in roleOptions"
                :key="role.value"
                :label="role.label"
                :value="role.value"
              />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button text size="small" type="danger" @click="removeMember(row)">移出</el-button>
          </template>
        </el-table-column>
        <template #empty><el-empty description="还没有成员" /></template>
      </el-table>
    </el-drawer>
  </div>
</template>
