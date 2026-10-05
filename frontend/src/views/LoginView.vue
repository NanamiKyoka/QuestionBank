<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { ApiError } from '@/api/http'
import { useAuthStore } from '@/stores/auth'
import AuthShell from '@/components/ui/AuthShell.vue'
import ThemeToggle from '@/components/ui/ThemeToggle.vue'

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
  <AuthShell title="QuestionMind" subtitle="AI 原生题库与组卷系统">
    <template #corner><ThemeToggle /></template>

    <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent>
      <el-form-item label="用户名" prop="username">
        <el-input
          v-model="form.username"
          placeholder="请输入用户名"
          size="large"
          autofocus
          autocomplete="username"
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
          autocomplete="current-password"
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
  </AuthShell>
</template>
