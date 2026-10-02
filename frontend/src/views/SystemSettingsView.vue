<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { settingsApi, type SystemSettingItem } from '@/api/settings'

/**
 * 系统设置。仅全局管理员。
 *
 * 这个页面刻意不做"通用键值编辑器"。
 *
 * system_settings 表里有一批**只声明、没有任何代码读取**的键
 * （default_review_threshold、ai_extract_prompt / ai_solve_prompt）。
 * 参考项目的设置页是按 key 逐条渲染输入框的，照搬过来会得到一个
 * 看起来能改、实际上改了不起作用的界面——管理员保存成功、关掉页面、
 * 然后发现行为一点没变，这类"沉默的失败"比功能缺失更难查。
 *
 * 因此后端只把**确实有读取方**的键标成 editable，其余一律只读并写明原因
 * （见 service/settings.go 的 settingCatalog），这里照着呈现，不额外发挥。
 */

const loading = ref(false)
const savingKey = ref<string | null>(null)
const items = ref<SystemSettingItem[]>([])

/** 每个可写项的本地草稿。用 key 做索引，避免给只读项也准备一份输入状态。 */
const drafts = ref<Record<string, string>>({})

const editableItems = computed(() => items.value.filter((i) => i.editable))
const readOnlyItems = computed(() => items.value.filter((i) => !i.editable))

onMounted(load)

async function load() {
  loading.value = true
  try {
    items.value = await settingsApi.list()
    // 草稿跟随当前值初始化：重新拉取后不保留上一次的输入，
    // 免得管理员对着一个过期的界面点保存。
    const next: Record<string, string> = {}
    for (const item of items.value) {
      next[item.key] = item.value
    }
    drafts.value = next
  } finally {
    loading.value = false
  }
}

function draft(item: SystemSettingItem): string {
  return drafts.value[item.key] ?? item.value
}

function isBool(item: SystemSettingItem): boolean {
  return item.type === 'bool'
}

function toBoolText(value: string): string {
  return ['true', '1', 'yes', 'on'].includes(value.trim().toLowerCase()) ? 'true' : 'false'
}

/** 把 bool 开关的状态翻译成后端接受的字面量。 */
function boolFromSwitch(value: string | number | boolean): string {
  return value ? 'true' : 'false'
}

async function save(item: SystemSettingItem) {
  const raw = draft(item)
  if (isBool(item) && !['true', 'false'].includes(raw)) {
    ElMessage.warning('布尔设置只能取 true 或 false')
    return
  }
  if (raw === item.value) {
    ElMessage.info('没有改动')
    return
  }

  savingKey.value = item.key
  try {
    await settingsApi.update(item.key, raw)
    ElMessage.success('已保存')
  } catch {
    // 保留草稿：用户填的东西不该因为一次失败就没了。
    return
  } finally {
    savingKey.value = null
  }
  await load()
}

async function resetToDefault(item: SystemSettingItem) {
  try {
    await ElMessageBox.confirm(
      `确定把「${item.label}」恢复成默认值 ${item.def} 吗？`,
      '恢复默认',
      { type: 'warning', confirmButtonText: '恢复', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  savingKey.value = item.key
  try {
    await settingsApi.update(item.key, item.def)
    ElMessage.success('已恢复默认')
  } catch {
    return
  } finally {
    savingKey.value = null
  }
  await load()
}

function formatTime(value: string | null): string {
  if (!value) return '从未修改'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN')
}

/** 只读项的值展示：长文本（提示词）截断，避免把表格撑爆。 */
function displayValue(item: SystemSettingItem): string {
  if (!item.value) return '（空）'
  if (item.type === 'bool') {
    return ['true', '1', 'yes', 'on'].includes(item.value.trim().toLowerCase()) ? '开启' : '关闭'
  }
  if (item.type === 'text' && item.value.length > 80) {
    return `${item.value.slice(0, 80)}…`
  }
  return item.value
}
</script>

<template>
  <div v-loading="loading" class="space-y-3">
    <el-alert
      type="info"
      show-icon
      :closable="false"
      title="这里只列出系统真正会读取的设置项。"
      description="AI 供应商、密钥与默认模型在「AI 设置」页维护；端口、数据库地址等启动级配置在配置文件中，改动需要重启服务。"
    />

    <!-- 可修改 -->
    <el-card shadow="never">
      <template #header>
        <span class="text-sm font-medium">生效中的设置</span>
      </template>

      <el-empty v-if="!loading && editableItems.length === 0" description="没有可修改的设置项" />

      <div
        v-for="item in editableItems"
        :key="item.key"
        class="border-b border-hairline py-3 last:border-b-0"
      >
        <div class="flex flex-wrap items-center gap-3">
          <div class="min-w-48 flex-1">
            <div class="flex items-center gap-2">
              <span class="font-medium">{{ item.label }}</span>
              <el-tag size="small" type="success" effect="plain">生效中</el-tag>
              <el-tag v-if="!item.saved" size="small" type="info" effect="plain">使用默认值</el-tag>
            </div>
            <div class="mt-0.5 text-xs text-ink-subtle">
              {{ item.description }} · <code>{{ item.key }}</code>
            </div>
          </div>

          <el-switch
            v-if="isBool(item)"
            :model-value="toBoolText(draft(item)) === 'true'"
            :loading="savingKey === item.key"
            @update:model-value="drafts[item.key] = boolFromSwitch($event)"
          />
          <el-input
            v-else
            v-model="drafts[item.key]"
            class="w-72"
            :placeholder="item.def ? `默认 ${item.def}` : '留空'"
          />

          <div class="flex gap-1">
            <el-button
              type="primary"
              size="small"
              :loading="savingKey === item.key"
              @click="save(item)"
            >
              保存
            </el-button>
            <el-button
              v-if="item.value !== item.def"
              size="small"
              @click="resetToDefault(item)"
            >
              恢复默认
            </el-button>
          </div>
        </div>

        <div class="mt-1 text-xs text-ink-subtle">
          最后修改：{{ formatTime(item.updated_at) }}
        </div>
        <el-alert
          v-if="item.note"
          class="mt-2"
          type="warning"
          :closable="false"
          :title="item.note"
        />
      </div>
    </el-card>

    <!-- 只读 -->
    <el-card shadow="never">
      <template #header>
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium">只读设置项</span>
          <el-tooltip
            placement="top"
            content="这些键要么由别的设置页管理，要么当前没有任何代码读取。列出来是为了让「表里有这一行、界面上却没有」这种事不再发生。"
          >
            <el-icon class="text-ink-subtle"><QuestionFilled /></el-icon>
          </el-tooltip>
        </div>
      </template>

      <el-empty v-if="!loading && readOnlyItems.length === 0" description="没有只读设置项" />

      <el-table v-else :data="readOnlyItems" row-key="key" size="small">
        <el-table-column prop="label" label="名称" min-width="160">
          <template #default="{ row }">
            <div class="flex items-center gap-2">
              <span>{{ row.label }}</span>
              <el-tag size="small" type="info" effect="plain">只读</el-tag>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="当前值" min-width="200">
          <template #default="{ row }">
            <span class="text-sm">{{ displayValue(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="说明" min-width="320">
          <template #default="{ row }">
            <span class="text-xs text-ink-muted">{{ row.note }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最后修改" width="180">
          <template #default="{ row }">
            <span class="text-xs text-ink-subtle">{{ formatTime(row.updated_at) }}</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>
