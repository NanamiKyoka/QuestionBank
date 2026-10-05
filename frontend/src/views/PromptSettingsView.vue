<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { subjectApi } from '@/api'
import type { Subject } from '@/api/types'
import PageHeader from '@/components/ui/PageHeader.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { promptApi, type PromptCatalogEntry, type SubjectPrompt, type SubjectPrompts } from '@/api/prompt'
import { useFormDirty, useUnsavedGuard } from '@/composables/useUnsavedGuard'

/**
 * 提示词设置。仅全局管理员。
 *
 * 页面刻意按"解析链"组织，而不是做成一个通用的键值编辑器：
 * 学科覆盖 → 全局模板 → 系统设置兜底 → 代码内置默认。每一栏都标出
 * 当前**实际生效**的是哪一级，以及"为什么没用你以为在用的那一级"。
 *
 * 理由很实际：提示词是最容易出现"保存成功了、行为却没变"的配置。
 * 一个只显示输入框的界面会让人以为改了就生效；而在这里，
 * "正在使用内置默认"和"配置了但没命中"是两件肉眼可见的事。
 */

// ---- 加载 ----

const loading = ref(false)
const entries = ref<PromptCatalogEntry[]>([])
const subjects = ref<Subject[]>([])

/** 全局模板草稿：key → 正文。用草稿而不是直接改 item，取消编辑才有意义。 */
const globalDrafts = ref<Record<string, string>>({})
const savingKeys = ref<string[]>([])

/** 学科覆盖 */
const currentSubject = ref<number | null>(null)
const subjectData = ref<SubjectPrompts | null>(null)
const subjectLoading = ref(false)
const subjectSaving = ref(false)
/** 学科覆盖草稿：key → 正文。空串表示"删掉这条覆盖"。 */
const subjectDrafts = ref<Record<string, string>>({})

// 全局模板与学科覆盖是两份独立草稿，但"离开"只看有没有任何一份没保存。
const globalDirty = useFormDirty(() => globalDrafts.value)
const subjectDirty = useFormDirty(() => subjectDrafts.value)
useUnsavedGuard(
  () => globalDirty.dirty.value || subjectDirty.dirty.value,
  '提示词有还没保存的修改，确定离开吗？',
)

const hasGlobalOverride = computed(() =>
  entries.value.some((item) => item.template !== null),
)
const hasSubjectOverride = computed(() =>
  (subjectData.value?.prompts ?? []).some((item) => item.override_id > 0),
)

onMounted(async () => {
  loading.value = true
  try {
    const [list, subjectList] = await Promise.all([promptApi.list(), subjectApi.list()])
    entries.value = list.items
    subjects.value = subjectList
    resetGlobalDrafts()
    // 默认落在第一个学科上：让管理员一进来就能看到"这个学科被覆盖了什么"，
    // 而不是面对一个空的选择框。
    const first = subjectList[0]
    if (first) {
      currentSubject.value = first.id
    }
  } finally {
    loading.value = false
  }
})

watch(currentSubject, (id) => {
  if (id) void loadSubject(id)
  else {
    subjectData.value = null
    subjectDrafts.value = {}
    subjectDirty.markClean()
  }
})

function resetGlobalDrafts() {
  const next: Record<string, string> = {}
  for (const item of entries.value) {
    next[item.key] = item.template?.system_prompt ?? ''
  }
  globalDrafts.value = next
  // 刚灌进来的模板是新的原点，别把它算成"没保存的修改"。
  globalDirty.markClean()
}

async function loadSubject(id: number) {
  subjectLoading.value = true
  try {
    const data = await promptApi.subjectList(id)
    subjectData.value = data
    const next: Record<string, string> = {}
    for (const item of data.prompts) {
      // 有覆盖时，effective 就是这条覆盖的原始模板（接口只在 preview 里给渲染结果）；
      // 没有覆盖时留空，明确区分"编辑这条覆盖"与"删掉这条覆盖"。
      next[item.key] = item.override_id > 0 ? item.effective : ''
    }
    subjectDrafts.value = next
    subjectDirty.markClean()
  } finally {
    subjectLoading.value = false
  }
}

function draft(key: string): string {
  return globalDrafts.value[key] ?? ''
}

function subjectDraft(key: string): string {
  return subjectDrafts.value[key] ?? ''
}

function isBusy(key: string): boolean {
  return savingKeys.value.includes(key)
}

// ---- 全局模板 ----

async function saveGlobal(item: PromptCatalogEntry) {
  const body = draft(item.key).trim()
  if (!body) {
    ElMessage.warning('提示词不能为空；想恢复默认请用「删除模板」')
    return
  }
  savingKeys.value = [...savingKeys.value, item.key]
  try {
    if (item.template) {
      await promptApi.update(item.template.id, body)
    } else {
      await promptApi.create({ key: item.key, system_prompt: body })
    }
    ElMessage.success('已保存')
  } catch {
    // 保留草稿：管理员写的东西不该因为一次失败就没了。
    return
  } finally {
    savingKeys.value = savingKeys.value.filter((k) => k !== item.key)
  }
  await reload()
}

async function removeGlobal(item: PromptCatalogEntry) {
  if (!item.template) return
  try {
    await ElMessageBox.confirm(
      `删除后「${item.name}」将回退到内置默认（若系统设置里配过兜底正文，则回退到那一级）。确定删除吗？`,
      '删除全局模板',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  savingKeys.value = [...savingKeys.value, item.key]
  try {
    await promptApi.remove(item.template.id)
    ElMessage.success('已删除，正在使用下一级来源')
  } catch {
    return
  } finally {
    savingKeys.value = savingKeys.value.filter((k) => k !== item.key)
  }
  await reload()
}

async function reload() {
  const list = await promptApi.list()
  entries.value = list.items
  resetGlobalDrafts()
}

// ---- 学科覆盖 ----

async function saveSubject() {
  const id = currentSubject.value
  if (!id || !subjectData.value) return
  subjectSaving.value = true
  try {
    const data = await promptApi.subjectSave(
      id,
      subjectData.value.prompts.map((item) => ({
        key: item.key,
        system_prompt: subjectDraft(item.key),
      })),
    )
    subjectData.value = data
    // 重新按保存结果重置草稿：空串表示已删除覆盖，有覆盖则带上它的正文。
    const next: Record<string, string> = {}
    for (const item of data.prompts) {
      next[item.key] = item.override_id > 0 ? item.effective : ''
    }
    subjectDrafts.value = next
    subjectDirty.markClean()
    ElMessage.success('已保存该学科的提示词')
  } finally {
    subjectSaving.value = false
  }
}

function clearSubjectOverride(item: SubjectPrompt) {
  if (item.override_id === 0) return
  ElMessage.info('已清空草稿，点击「保存本学科的覆盖」后回退到全局模板')
  subjectDrafts.value = { ...subjectDrafts.value, [item.key]: '' }
}

// ---- 展示辅助 ----

const sourceLabel: Record<string, string> = {
  subject: '学科覆盖',
  global: '全局模板',
  setting: '系统设置兜底',
  builtin: '内置默认',
}

const sourceTagType: Record<string, 'success' | 'info' | 'warning' | 'danger'> = {
  subject: 'success',
  global: 'success',
  setting: 'warning',
  builtin: 'info',
}

function sourceText(source: string): string {
  return sourceLabel[source] ?? source
}

function sourceTag(source: string): 'success' | 'info' | 'warning' | 'danger' {
  return sourceTagType[source] ?? 'info'
}

function formatTime(value: string | null): string {
  if (!value) return '从未配置'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN')
}

/** 把占位符插到光标处。textarea 没有暴露选区，只能追加到末尾。 */
function insertToken(key: string, token: string) {
  globalDrafts.value = { ...globalDrafts.value, [key]: `${draft(key)}${token}` }
}

function insertSubjectToken(key: string, token: string) {
  subjectDrafts.value = { ...subjectDrafts.value, [key]: `${subjectDraft(key)}${token}` }
}
</script>

<template>
  <div v-loading="loading" class="space-y-3">
    <PageHeader title="提示词" description="决定智能录题与 AI 助手的抽取与对话行为。留空表示未配置，正在使用内置默认。" />

    <el-alert
      type="info"
      show-icon
      :closable="false"
      title="提示词按「学科覆盖 → 全局模板 → 系统设置兜底 → 内置默认」四级解析。"
      description="任何一级缺失或不可用都会自动回退到下一级，因此这里显示的来源就是导入与对话时真正使用的那一份。留空表示未配置，正在使用内置默认。"
    />

    <el-alert
      v-if="!hasGlobalOverride"
      type="warning"
      show-icon
      :closable="false"
      title="目前全部使用内置默认提示词"
      description="这套内置提示词已经过调校，直接投入使用通常没有问题。只有当抽取结果与你的学科要求不符时才需要改。"
    />

    <!-- 全局模板 -->
    <el-card v-for="item in entries" :key="item.key" shadow="never">
      <template #header>
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">{{ item.name }}</span>
          <el-tag size="small" effect="plain" :type="sourceTag(item.effective_source)">
            生效来源：{{ sourceText(item.effective_source) }}
          </el-tag>
          <el-tag v-if="item.template" size="small" type="success" effect="plain">已配置</el-tag>
          <el-tag v-else size="small" type="info" effect="plain">未配置</el-tag>
          <el-tag v-if="item.override_count > 0" size="small" type="warning" effect="plain">
            {{ item.override_count }} 个学科有覆盖
          </el-tag>
          <code class="text-xs text-ink-subtle">{{ item.key }}</code>
        </div>
      </template>

      <p class="mb-2 text-xs text-ink-muted">{{ item.description }}</p>

      <div class="mb-2 flex flex-wrap items-center gap-2">
        <span class="text-xs text-ink-subtle">可用占位符：</span>
        <el-tag
          v-for="p in item.placeholders"
          :key="p.name"
          size="small"
          effect="plain"
          class="cursor-pointer"
          @click="insertToken(item.key, p.token)"
        >
          {{ p.token }}
        </el-tag>
        <el-tooltip placement="top">
          <template #content>
            一行里只要有一个占位符取值为空，整行会被删除——这是做"可选段落"的方式。<br />
            因此必须保留的固定文字不要和可能为空的占位符写在同一行。
          </template>
          <span class="text-xs text-ink-subtle underline decoration-dotted">规则说明</span>
        </el-tooltip>
      </div>

      <el-input
        v-model="globalDrafts[item.key]"
        type="textarea"
        :rows="10"
        :placeholder="item.default"
        class="font-mono text-xs"
      />

      <div class="mt-2 flex flex-wrap items-center gap-2">
        <el-button type="primary" size="small" :loading="isBusy(item.key)" @click="saveGlobal(item)">
          保存全局模板
        </el-button>
        <el-button
          v-if="item.template"
          size="small"
          :loading="isBusy(item.key)"
          @click="removeGlobal(item)"
        >
          删除模板
        </el-button>
        <el-button size="small" @click="globalDrafts[item.key] = item.default">
          填入内置默认
        </el-button>
        <span v-if="item.template" class="text-xs text-ink-subtle">
          最后修改：{{ formatTime(item.template.updated_at) }}
        </span>
      </div>

      <el-alert
        v-if="item.fallback"
        class="mt-2"
        type="warning"
        :closable="false"
        :title="item.fallback"
      />
      <el-alert
        v-if="item.setting_key && item.setting_value"
        class="mt-2"
        type="info"
        :closable="false"
        :title="`系统设置 ${item.setting_key} 里也有一份正文，仅在未配置全局模板时才会被使用。`"
      />
    </el-card>

    <!-- 学科覆盖 -->
    <el-card v-loading="subjectLoading" shadow="never">
      <template #header>
        <span class="text-sm font-medium">按学科覆盖</span>
      </template>

      <div class="mb-3 flex items-center gap-3">
        <span class="text-xs text-ink-subtle">学科</span>
        <el-select v-model="currentSubject" class="w-56" placeholder="选择学科">
          <el-option v-for="s in subjects" :key="s.id" :label="s.name" :value="s.id" />
        </el-select>
        <el-tag v-if="hasSubjectOverride" size="small" type="warning" effect="plain">
          该学科有覆盖
        </el-tag>
      </div>

      <EmptyState v-if="!subjectData" description="请选择学科" />

      <div v-else class="space-y-3">
        <p class="text-xs text-ink-muted">
          留空表示该学科使用全局模板。全局模板也没配时使用内置默认。
        </p>

        <div
          v-for="item in subjectData.prompts"
          :key="item.key"
          class="border-b border-hairline pb-3 last:border-b-0"
        >
          <div class="mb-1 flex flex-wrap items-center gap-2">
            <span class="text-sm">{{ item.name }}</span>
            <el-tag size="small" effect="plain" :type="sourceTag(item.source)">
              本学科实际使用：{{ sourceText(item.source) }}
            </el-tag>
            <el-tag v-if="item.override_id > 0" size="small" type="success" effect="plain">
              已覆盖
            </el-tag>
            <span v-if="item.override_updated_at" class="text-xs text-ink-subtle">
              最后修改：{{ formatTime(item.override_updated_at) }}
            </span>
          </div>

          <div class="mb-1 flex flex-wrap items-center gap-2">
            <el-tag
              v-for="p in item.placeholders"
              :key="p.name"
              size="small"
              effect="plain"
              class="cursor-pointer"
              @click="insertSubjectToken(item.key, p.token)"
            >
              {{ p.token }}
            </el-tag>
          </div>

          <el-input
            v-model="subjectDrafts[item.key]"
            type="textarea"
            :rows="4"
            placeholder="留空 = 使用全局模板"
            class="font-mono text-xs"
          />

          <div class="mt-1 flex gap-2">
            <el-button size="small" @click="subjectDrafts[item.key] = item.effective">
              填入当前生效的版本
            </el-button>
            <el-button
              v-if="item.override_id > 0"
              size="small"
              @click="clearSubjectOverride(item)"
            >
              清空并回退到全局
            </el-button>
          </div>
        </div>

        <div class="flex gap-2">
          <el-button type="primary" :loading="subjectSaving" @click="saveSubject">
            保存本学科的覆盖
          </el-button>
          <el-button @click="subjectData && loadSubject(subjectData.subject_id)">放弃修改</el-button>
        </div>
      </div>
    </el-card>
  </div>
</template>
