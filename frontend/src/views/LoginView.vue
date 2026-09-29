<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { ApiError } from '@/api/http'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const formRef = ref<FormInstance>()
const loading = ref(false)
const form = reactive({ username: '', password: '' })

const rules: FormRules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }],
}

async function onSubmit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    await auth.login(form.username, form.password)
    ElMessage.success('登录成功')
    // 登录前想去的页面优先，否则回首页。
    const redirect = (route.query.redirect as string) || '/'
    await router.replace(redirect)
  } catch (error) {
    // 具体原因（用户名或密码错误 / 账号被禁用）由接口层弹出，
    // 这里不重复提示，只把密码清掉让用户重输。
    if (error instanceof ApiError) form.password = ''
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="flex h-full items-center justify-center bg-gradient-to-br from-slate-50 to-blue-50">
    <el-card class="w-[400px] shadow-lg" shadow="never">
      <div class="mb-6 text-center">
        <div
          class="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-blue-600 text-lg font-bold text-white"
        >
          QM
        </div>
        <h1 class="text-xl font-semibold">QuestionMind</h1>
        <p class="mt-1 text-sm text-gray-500">AI 原生题库与组卷系统</p>
      </div>

      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent>
        <el-form-item label="用户名" prop="username">
          <el-input
            v-model="form.username"
            placeholder="请输入用户名"
            size="large"
            autofocus
            @keyup.enter="onSubmit"
          />
        </el-form-item>

        <el-form-item label="密码" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="请输入密码"
            size="large"
            show-password
            @keyup.enter="onSubmit"
          />
        </el-form-item>

        <el-button
          type="primary"
          size="large"
          class="mt-2 w-full"
          :loading="loading"
          @click="onSubmit"
        >
          登录
        </el-button>
      </el-form>
    </el-card>
  </div>
</template>
