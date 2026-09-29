<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { systemApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { markSetupComplete } from '@/router'

/**
 * 首次运行向导。
 *
 * 后端在未初始化时会把所有 /api 请求挡成 503 setup_required，
 * 因此这个页面是唯一能用的入口（除了探活与版本查询）。
 */
const router = useRouter()
const auth = useAuthStore()

const formRef = ref<FormInstance>()
const loading = ref(false)

const form = reactive({
  username: '',
  password: '',
  confirm: '',
  nickname: '',
  first_subject_name: '',
})

const rules: FormRules = {
  username: [
    { required: true, message: '请输入用户名', trigger: 'blur' },
    { min: 3, max: 64, message: '长度 3-64 个字符', trigger: 'blur' },
    {
      pattern: /^[a-zA-Z0-9]+$/,
      message: '只能包含字母和数字',
      trigger: 'blur',
    },
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 8, max: 72, message: '长度 8-72 个字符', trigger: 'blur' },
  ],
  confirm: [
    {
      validator: (_rule, value: string, callback) => {
        if (!value) return callback(new Error('请再次输入密码'))
        if (value !== form.password) return callback(new Error('两次输入的密码不一致'))
        callback()
      },
      trigger: 'blur',
    },
  ],
}

async function onSubmit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    const result = await systemApi.setup({
      username: form.username,
      password: form.password,
      nickname: form.nickname || form.username,
      first_subject_name: form.first_subject_name,
    })
    // 后端在初始化成功时直接返回了 token，省掉一次登录。
    auth.setToken(result.access_token)
    await auth.restore()
    markSetupComplete()
    ElMessage.success('初始化完成，欢迎使用')
    await router.replace('/')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="flex h-full items-center justify-center bg-gradient-to-br from-slate-50 to-blue-50">
    <el-card class="w-[480px] shadow-lg" shadow="never">
      <div class="mb-6 text-center">
        <h1 class="text-xl font-semibold">初始化 QuestionMind</h1>
        <p class="mt-1 text-sm text-gray-500">
          创建第一个管理员账号。完成后即可登录使用。
        </p>
      </div>

      <el-form ref="formRef" :model="form" :rules="rules" label-position="top">
        <el-form-item label="管理员用户名" prop="username">
          <el-input v-model="form.username" placeholder="字母或数字，如 admin" />
        </el-form-item>

        <el-form-item label="显示名称">
          <el-input v-model="form.nickname" placeholder="选填，默认为用户名" />
        </el-form-item>

        <el-form-item label="密码" prop="password">
          <el-input v-model="form.password" type="password" show-password placeholder="至少 8 位" />
        </el-form-item>

        <el-form-item label="确认密码" prop="confirm">
          <el-input v-model="form.confirm" type="password" show-password />
        </el-form-item>

        <el-form-item label="默认学科">
          <el-input v-model="form.first_subject_name" placeholder="选填，如「数学」" />
          <div class="mt-1 text-xs text-gray-400">
            填写后会自动创建该学科，并把管理员设为学科负责人。
          </div>
        </el-form-item>

        <el-button
          type="primary"
          size="large"
          class="mt-2 w-full"
          :loading="loading"
          @click="onSubmit"
        >
          完成初始化
        </el-button>
      </el-form>
    </el-card>
  </div>
</template>
