<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { profileApi } from '@/api/profile'
import { useAuthStore } from '@/stores/auth'
import { useSubjectStore } from '@/stores/subject'

/**
 * 个人资料与密码。
 *
 * 三个后端端点早就存在（router.go:172-174），只是界面上没有入口：
 *   PATCH /auth/me                  改昵称 / 邮箱 / 头像
 *   POST  /auth/me/password         改密码（需校验旧密码）
 *   POST  /auth/me/last-subject     记住上次登录的学科
 * 这里把它们第一次接上。
 */

const auth = useAuthStore()
const subject = useSubjectStore()

const loading = ref(false)
const savingProfile = ref(false)
const savingPassword = ref(false)
const savingSubject = ref(false)

const form = reactive({
  nickname: '',
  email: '',
  avatar: '',
})

const pwd = reactive({
  current: '',
  next: '',
  confirm: '',
})

// 密码策略与后端 schema/auth.go:141-146 保持一致：8–72 位。
// 72 这个上限来自 bcrypt 的 72 字节截断，再长会被静默忽略，
// 后端因此显式拒绝，这里提前拦下并说明原因。
const PASSWORD_MIN = 8
const PASSWORD_MAX = 72

const passwordIssue = computed<string | null>(() => {
  if (!pwd.next) return null
  if (pwd.next.length < PASSWORD_MIN) return `新密码至少 ${PASSWORD_MIN} 位`
  if (pwd.next.length > PASSWORD_MAX) {
    return `新密码最长 ${PASSWORD_MAX} 位：bcrypt 只取前 72 字节，再长会被静默截断`
  }
  return null
})

const lastSubjectId = ref<number | null>(null)

onMounted(async () => {
  // 布局已经拉过 /auth/me 了，这里只在缺资料时补一次，
  // 免得每进一次页面都发两个一模一样的请求。
  if (!auth.user) {
    loading.value = true
    try {
      auth.user = await profileApi.me()
    } finally {
      loading.value = false
    }
  }
  reset()
})

function reset() {
  form.nickname = auth.user?.nickname ?? ''
  form.email = auth.user?.email ?? ''
  form.avatar = auth.user?.avatar ?? ''
  pwd.current = ''
  pwd.next = ''
  pwd.confirm = ''
  lastSubjectId.value = auth.user?.last_subject_id ?? null
}

async function saveProfile() {
  if (!form.nickname.trim() && !form.email.trim()) {
    ElMessage.warning('昵称与邮箱至少填一项')
    return
  }
  if (form.email.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(form.email.trim())) {
    ElMessage.warning('邮箱格式不正确')
    return
  }

  savingProfile.value = true
  try {
    // 只提交改过的字段：后端用指针区分"不传"与"传空串"，
    // 把没改的也带上会把"清空邮箱"和"保持不变"混成一件事。
    const payload: { nickname?: string; email?: string; avatar?: string } = {}
    const nickname = form.nickname.trim()
    const email = form.email.trim()
    const avatar = form.avatar.trim()
    if (nickname !== (auth.user?.nickname ?? '')) payload.nickname = nickname
    if (email !== (auth.user?.email ?? '')) payload.email = email
    if (avatar !== (auth.user?.avatar ?? '')) payload.avatar = avatar

    if (Object.keys(payload).length === 0) {
      ElMessage.info('没有改动')
      return
    }
    // 写回 store，头像与昵称立刻生效，不必等下一次刷新。
    auth.user = await profileApi.updateMe(payload)
    ElMessage.success('已保存')
  } finally {
    savingProfile.value = false
  }
}

async function changePassword() {
  if (!pwd.current) {
    ElMessage.warning('请输入当前密码')
    return
  }
  if (passwordIssue.value) {
    ElMessage.warning(passwordIssue.value)
    return
  }
  if (pwd.next === pwd.current) {
    ElMessage.warning('新密码不能与当前密码相同')
    return
  }
  if (pwd.next !== pwd.confirm) {
    ElMessage.warning('两次输入的新密码不一致')
    return
  }

  savingPassword.value = true
  try {
    await profileApi.changePassword({
      current_password: pwd.current,
      new_password: pwd.next,
    })
  } finally {
    savingPassword.value = false
  }
  pwd.current = ''
  pwd.next = ''
  pwd.confirm = ''
  ElMessage.success('密码已修改')
}

async function saveLastSubject() {
  if (lastSubjectId.value === null) return
  savingSubject.value = true
  try {
    await profileApi.setLastSubject(lastSubjectId.value)
    // 同步本地记忆：下次进来选中的学科要和后端记住的一致。
    localStorage.setItem('qm_last_subject', String(lastSubjectId.value))
    if (auth.user) {
      auth.user = { ...auth.user, last_subject_id: lastSubjectId.value }
    }
    ElMessage.success('已保存')
  } finally {
    savingSubject.value = false
  }
}

async function clearLastSubject() {
  try {
    await ElMessageBox.confirm(
      '下次登录时将回到第一个可见的学科。',
      '清除默认学科',
      { type: 'warning', confirmButtonText: '清除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  localStorage.removeItem('qm_last_subject')
  lastSubjectId.value = null
  if (auth.user) {
    auth.user = { ...auth.user, last_subject_id: null }
  }
  ElMessage.success('已清除')
}

function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN')
}
</script>

<template>
  <div v-loading="loading" class="space-y-3">
    <el-row :gutter="12">
      <!-- 个人资料 -->
      <el-col :xs="24" :md="14">
        <el-card shadow="never">
          <template #header>
            <span class="text-sm font-medium">个人资料</span>
          </template>

          <div class="mb-4 flex items-center gap-3">
            <el-avatar :size="56" :src="form.avatar || undefined">
              {{ (auth.displayName || '?').slice(0, 1) }}
            </el-avatar>
            <div>
              <div class="font-medium">{{ auth.displayName || '未设置昵称' }}</div>
              <div class="text-xs text-ink-subtle">@{{ auth.user?.username }}</div>
            </div>
          </div>

          <el-form label-width="90px">
            <el-form-item label="显示名称">
              <el-input v-model="form.nickname" maxlength="64" placeholder="留空则显示用户名" />
            </el-form-item>
            <el-form-item label="邮箱">
              <el-input v-model="form.email" maxlength="128" placeholder="选填" />
            </el-form-item>
            <el-form-item label="头像地址">
              <el-input v-model="form.avatar" maxlength="255" placeholder="图片 URL，选填" />
              <div class="mt-1 text-xs text-ink-subtle">
                粘贴一个图片 URL 即可；没有就显示昵称首字。
              </div>
            </el-form-item>
          </el-form>

          <div class="flex justify-end gap-2">
            <el-button @click="reset">重置</el-button>
            <el-button type="primary" :loading="savingProfile" @click="saveProfile">
              保存
            </el-button>
          </div>
        </el-card>
      </el-col>

      <!-- 修改密码 -->
      <el-col :xs="24" :md="10">
        <el-card shadow="never">
          <template #header>
            <span class="text-sm font-medium">修改密码</span>
          </template>

          <el-form label-width="90px">
            <el-form-item label="当前密码" required>
              <el-input
                v-model="pwd.current"
                type="password"
                show-password
                autocomplete="current-password"
              />
            </el-form-item>
            <el-form-item label="新密码" required :error="passwordIssue ?? undefined">
              <el-input
                v-model="pwd.next"
                type="password"
                show-password
                autocomplete="new-password"
                :placeholder="`${PASSWORD_MIN}–${PASSWORD_MAX} 位`"
              />
            </el-form-item>
            <el-form-item label="确认新密码" required>
              <el-input
                v-model="pwd.confirm"
                type="password"
                show-password
                autocomplete="new-password"
              />
            </el-form-item>
          </el-form>

          <el-alert
            type="info"
            :closable="false"
            show-icon
            class="mb-3"
            title="修改密码不会让其他设备上的已登录状态立即失效。"
            description="本系统使用无状态 JWT，没有服务端吊销表，旧凭证会一直有效到自然过期。"
          />

          <div class="flex justify-end">
            <el-button type="primary" :loading="savingPassword" @click="changePassword">
              修改密码
            </el-button>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 账号信息 -->
    <el-card shadow="never">
      <template #header>
        <span class="text-sm font-medium">账号信息</span>
      </template>

      <el-descriptions :column="2" border size="small">
        <el-descriptions-item label="用户名">
          {{ auth.user?.username }}
        </el-descriptions-item>
        <el-descriptions-item label="角色">
          <el-tag size="small" :type="auth.isSuperuser ? 'danger' : 'info'">
            {{ auth.isSuperuser ? '全局管理员' : '普通用户' }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="上次登录">
          {{ formatTime(auth.user?.last_login_at) }}
        </el-descriptions-item>
        <el-descriptions-item label="登录次数">
          {{ auth.user?.login_count ?? 0 }}
        </el-descriptions-item>
        <el-descriptions-item label="注册时间">
          {{ formatTime(auth.user?.created_at) }}
        </el-descriptions-item>
        <el-descriptions-item label="所属学科">
          <el-tag
            v-for="m in auth.user?.memberships ?? []"
            :key="m.subject_id"
            size="small"
            class="mr-1"
          >
            {{ m.subject_name }} · {{ m.role_label }}
          </el-tag>
          <span v-if="(auth.user?.memberships ?? []).length === 0" class="text-ink-subtle">
            尚未加入任何学科
          </span>
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <!-- 默认学科 -->
    <el-card v-if="subject.subjects.length > 0" shadow="never">
      <template #header>
        <span class="text-sm font-medium">默认学科</span>
      </template>

      <div class="flex flex-wrap items-center gap-3">
        <el-select v-model="lastSubjectId" placeholder="不指定" clearable class="w-64">
          <el-option
            v-for="s in subject.subjects"
            :key="s.id"
            :label="s.name"
            :value="s.id"
          />
        </el-select>
        <el-button
          type="primary"
          size="small"
          :disabled="lastSubjectId === null"
          :loading="savingSubject"
          @click="saveLastSubject"
        >
          保存
        </el-button>
        <el-button size="small" @click="clearLastSubject">清除</el-button>
        <span class="text-xs text-ink-subtle">
          登录后自动进入这个学科，省去每次在左上角重选。
        </span>
      </div>
    </el-card>
  </div>
</template>
