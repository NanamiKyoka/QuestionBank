<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { aiApi, type ActiveConfig, type ModelInfo, type ModelType, type Provider } from '@/api/ai'

/**
 * AI 设置。
 *
 * 密钥处理是这个页面最容易出错的地方：后端永不返回明文，只给掩码
 * （如 sk-abc…wxyz）。如果把掩码当成密钥显示并回传，就会把真实密钥
 * 覆盖成那串掩码。因此这里明确区分三种状态：
 *   未配置 → 输入框为空，提交时必填
 *   已配置 → 输入框为空并显示"已配置"提示，留空即不修改
 *   要换新 → 用户主动展开输入框并填写，提交时带上新明文
 */

const loading = ref(false)
const providers = ref<Provider[]>([])
const active = ref<ActiveConfig | null>(null)

const providerVisible = ref(false)
const editingProviderId = ref<number | null>(null)
const providerForm = reactive({
  name: '',
  interface_type: 'openai' as 'openai' | 'gemini',
  base_url: '',
  api_key: '',
  is_active: true,
})
/** 用户是否主动要更换密钥。 */
const replacingKey = ref(false)

const modelVisible = ref(false)
const editingModelId = ref<number | null>(null)
const modelForm = reactive({
  provider_id: 0,
  name: '',
  display_name: '',
  model_type: 'text' as ModelType,
  max_tokens: 4096,
  context_window: 0,
  is_active: true,
})

const testing = ref<number | null>(null)

/** 常用服务商的预填地址，省去用户查文档。 */
const presets = [
  { label: 'DeepSeek', base_url: 'https://api.deepseek.com/v1', models: ['deepseek-chat', 'deepseek-reasoner'] },
  { label: '阿里云百炼', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', models: ['qwen-plus', 'qwen-vl-plus', 'text-embedding-v3'] },
  { label: '智谱 AI', base_url: 'https://open.bigmodel.cn/api/paas/v4', models: ['glm-4-plus', 'embedding-3'] },
  { label: 'OpenAI', base_url: 'https://api.openai.com/v1', models: ['gpt-4o-mini', 'text-embedding-3-small'] },
  { label: 'Ollama（本地）', base_url: 'http://127.0.0.1:11434/v1', models: ['qwen2.5:7b', 'nomic-embed-text'] },
]

const allModels = computed(() => providers.value.flatMap((p) => p.models))

const modelsByType = computed(() => {
  const group: Record<ModelType, ModelInfo[]> = { text: [], vision: [], embedding: [] }
  for (const model of allModels.value) {
    group[model.model_type]?.push(model)
  }
  return group
})

onMounted(load)

async function load() {
  loading.value = true
  try {
    const [list, config] = await Promise.all([aiApi.listProviders(), aiApi.activeConfig()])
    providers.value = list ?? []
    active.value = config
  } finally {
    loading.value = false
  }
}

// ---- 供应商 ----

function openCreateProvider() {
  editingProviderId.value = null
  providerForm.name = ''
  providerForm.interface_type = 'openai'
  providerForm.base_url = ''
  providerForm.api_key = ''
  providerForm.is_active = true
  replacingKey.value = true // 新建时本来就该填密钥
  providerVisible.value = true
}

function openEditProvider(provider: Provider) {
  editingProviderId.value = provider.id
  providerForm.name = provider.name
  providerForm.interface_type = provider.interface_type
  providerForm.base_url = provider.base_url
  providerForm.api_key = ''
  providerForm.is_active = provider.is_active
  replacingKey.value = !provider.has_key
  providerVisible.value = true
}

function applyPreset(baseURL: string) {
  providerForm.base_url = baseURL
}

async function submitProvider() {
  if (!providerForm.name.trim()) {
    ElMessage.warning('请输入名称')
    return
  }
  if (!editingProviderId.value && !providerForm.api_key.trim()) {
    ElMessage.warning('请填写 API Key')
    return
  }

  if (editingProviderId.value) {
    await aiApi.updateProvider(editingProviderId.value, {
      name: providerForm.name.trim(),
      base_url: providerForm.base_url.trim(),
      is_active: providerForm.is_active,
      // 只有用户主动换密钥时才带该字段。
      // 留空表示不修改——绝不能把掩码回传，那会把真密钥覆盖掉。
      ...(replacingKey.value && providerForm.api_key.trim()
        ? { api_key: providerForm.api_key.trim() }
        : {}),
    })
    ElMessage.success('已保存')
  } else {
    await aiApi.createProvider({
      name: providerForm.name.trim(),
      interface_type: providerForm.interface_type,
      base_url: providerForm.base_url.trim(),
      api_key: providerForm.api_key.trim(),
      is_active: providerForm.is_active,
    })
    ElMessage.success('已创建')
  }
  providerVisible.value = false
  await load()
}

async function removeProvider(provider: Provider) {
  try {
    await ElMessageBox.confirm(
      `确定删除供应商「${provider.name}」吗？其下 ${provider.models.length} 个模型也会一并删除。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await aiProviderDelete(provider.id)
}

async function aiProviderDelete(id: number) {
  await aiApi.deleteProvider(id)
  ElMessage.success('已删除')
  await load()
}

async function testProvider(provider: Provider) {
  const model = provider.models.find((m) => m.model_type === 'text' && m.is_active)
  if (!model) {
    ElMessage.warning('该供应商下还没有启用的文本模型，无法测试')
    return
  }
  testing.value = provider.id
  try {
    const result = await aiApi.testProvider(provider.id, { model_id: model.id })
    if (result.ok) {
      ElMessage.success(`连通正常（${result.latency_ms}ms）：${result.reply || '模型有响应'}`)
    } else {
      // 失败原因原样展示：可能是密钥错、也可能是地址错或是额度问题，
      // 后端已经把上游的说明整理过了，这里再包一层反而丢信息。
      ElMessageBox.alert(result.error || '测试失败', '连通性测试失败', { type: 'error' })
    }
  } finally {
    testing.value = null
  }
}

// ---- 模型 ----

function openCreateModel(provider?: Provider) {
  editingModelId.value = null
  modelForm.provider_id = provider?.id ?? providers.value[0]?.id ?? 0
  modelForm.name = ''
  modelForm.display_name = ''
  modelForm.model_type = 'text'
  modelForm.max_tokens = 4096
  modelForm.context_window = 0
  modelForm.is_active = true
  modelVisible.value = true
}

function openEditModel(model: ModelInfo) {
  editingModelId.value = model.id
  modelForm.provider_id = model.provider_id
  modelForm.name = model.name
  modelForm.display_name = model.display_name
  modelForm.model_type = model.model_type
  modelForm.max_tokens = model.max_tokens
  modelForm.context_window = model.context_window
  modelForm.is_active = model.is_active
  modelVisible.value = true
}

async function submitModel() {
  if (!modelForm.provider_id) {
    ElMessage.warning('请选择供应商')
    return
  }
  if (!modelForm.name.trim()) {
    ElMessage.warning('请填写模型名')
    return
  }
  if (editingModelId.value) {
    await aiApi.updateModel(editingModelId.value, {
      name: modelForm.name.trim(),
      display_name: modelForm.display_name.trim(),
      model_type: modelForm.model_type,
      max_tokens: modelForm.max_tokens,
      context_window: modelForm.context_window,
      is_active: modelForm.is_active,
    })
    ElMessage.success('已保存')
  } else {
    await aiApi.createModel({
      provider_id: modelForm.provider_id,
      name: modelForm.name.trim(),
      display_name: modelForm.display_name.trim(),
      model_type: modelForm.model_type,
      max_tokens: modelForm.max_tokens,
      context_window: modelForm.context_window,
      is_active: modelForm.is_active,
    })
    ElMessage.success('已创建')
  }
  modelVisible.value = false
  await load()
}

async function removeModel(model: ModelInfo) {
  try {
    await ElMessageBox.confirm(`确定删除模型「${model.name}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  await aiApi.deleteModel(model.id)
  ElMessage.success('已删除')
  await load()
}

// ---- 当前生效配置 ----

const selection = reactive({
  text_model_id: 0,
  vision_model_id: 0,
  embedding_model_id: 0,
})

function syncSelection() {
  selection.text_model_id = active.value?.text_model_id ?? 0
  selection.vision_model_id = active.value?.vision_model_id ?? 0
  selection.embedding_model_id = active.value?.embedding_model_id ?? 0
}

async function saveActive() {
  // 0 表示清空。后端用指针区分"不传"与"清空"，
  // 这里始终显式传值，语义就是"以界面上的选择为准"。
  active.value = await aiApi.setActiveConfig({
    text_model_id: selection.text_model_id,
    vision_model_id: selection.vision_model_id,
    embedding_model_id: selection.embedding_model_id,
  })
  ElMessage.success('已保存')
  syncSelection()
}


function typeLabel(type: ModelType): string {
  return { text: '文本', vision: '视觉', embedding: '向量' }[type]
}

function typeTag(type: ModelType): 'primary' | 'success' | 'warning' {
  return { text: 'primary', vision: 'success', embedding: 'warning' }[type] as
    | 'primary'
    | 'success'
    | 'warning'
}

// 首次拿到配置后同步下拉选择。
watch(active, syncSelection, { immediate: true })
</script>

<template>
  <div v-loading="loading" class="space-y-3">
    <!-- 就绪状态 -->
    <el-alert
      v-if="active"
      :type="active.ready ? 'success' : 'warning'"
      :closable="false"
      show-icon
      :title="active.message"
    />

    <!-- 当前生效配置 -->
    <el-card shadow="never">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">默认模型</span>
          <el-button type="primary" size="small" @click="saveActive">保存</el-button>
        </div>
      </template>

      <el-row :gutter="12">
        <el-col v-for="item in [
          { key: 'text_model_id', label: '文本模型', hint: '智能录题、AI 助手', required: true },
          { key: 'vision_model_id', label: '视觉模型', hint: '图片识别录题', required: false },
          { key: 'embedding_model_id', label: '向量模型', hint: '知识点语义检索', required: false },
        ]" :key="item.key" :xs="24" :md="8">
          <div class="mb-3">
            <div class="mb-1 flex items-center gap-2">
              <span class="text-sm">{{ item.label }}</span>
              <el-tag v-if="item.required" size="small" type="danger" effect="plain">必需</el-tag>
            </div>
            <el-select
              v-model="selection[item.key as 'text_model_id']"
              placeholder="未配置"
              clearable
              class="w-full"
            >
              <el-option
                v-for="model in modelsByType[
                  item.key === 'text_model_id' ? 'text' : item.key === 'vision_model_id' ? 'vision' : 'embedding'
                ]"
                :key="model.id"
                :label="`${model.display_name || model.name}（${model.provider_name}）`"
                :value="model.id"
              />
            </el-select>
            <div class="mt-1 text-xs text-gray-400">{{ item.hint }}</div>
          </div>
        </el-col>
      </el-row>
    </el-card>

    <!-- 供应商 -->
    <el-card shadow="never">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">AI 供应商</span>
          <el-button type="primary" size="small" @click="openCreateProvider">
            <el-icon class="mr-1"><Plus /></el-icon>添加供应商
          </el-button>
        </div>
      </template>

      <el-empty v-if="providers.length === 0" description="还没有配置供应商">
        <el-button type="primary" @click="openCreateProvider">添加第一个供应商</el-button>
      </el-empty>

      <div v-for="provider in providers" :key="provider.id" class="mb-3 rounded border border-gray-200 p-3">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-medium">{{ provider.name }}</span>
          <el-tag size="small" :type="provider.interface_type === 'openai' ? 'primary' : 'success'">
            {{ provider.interface_type === 'openai' ? 'OpenAI 兼容' : 'Gemini' }}
          </el-tag>
          <el-tag v-if="!provider.is_active" size="small" type="info">已停用</el-tag>
          <el-tag size="small" :type="provider.has_key ? 'success' : 'danger'" effect="plain">
            {{ provider.has_key ? `密钥 ${provider.key_hint}` : '未配置密钥' }}
          </el-tag>

          <div class="ml-auto flex gap-1">
            <el-button text size="small" :loading="testing === provider.id" @click="testProvider(provider)">
              测试连通
            </el-button>
            <el-button text size="small" @click="openCreateModel(provider)">添加模型</el-button>
            <el-button text size="small" @click="openEditProvider(provider)">编辑</el-button>
            <el-button text size="small" type="danger" @click="removeProvider(provider)">删除</el-button>
          </div>
        </div>

        <div v-if="provider.base_url" class="mt-1 text-xs text-gray-400">{{ provider.base_url }}</div>

        <div v-if="provider.models.length" class="mt-2 space-y-1">
          <div
            v-for="model in provider.models"
            :key="model.id"
            class="flex items-center gap-2 rounded bg-gray-50 px-2 py-1 text-sm"
          >
            <el-tag size="small" :type="typeTag(model.model_type)">{{ typeLabel(model.model_type) }}</el-tag>
            <span>{{ model.name }}</span>
            <span v-if="model.display_name" class="text-xs text-gray-400">{{ model.display_name }}</span>
            <el-tag v-if="model.in_use" size="small" type="success" effect="plain">使用中</el-tag>
            <el-tag v-if="!model.is_active" size="small" type="info">已停用</el-tag>
            <div class="ml-auto flex gap-1">
              <el-button text size="small" @click="openEditModel(model)">编辑</el-button>
              <el-button text size="small" type="danger" @click="removeModel(model)">删除</el-button>
            </div>
          </div>
        </div>
        <div v-else class="mt-2 text-xs text-gray-400">还没有添加模型</div>
      </div>
    </el-card>

    <!-- 供应商编辑 -->
    <el-dialog
      v-model="providerVisible"
      :title="editingProviderId ? '编辑供应商' : '添加供应商'"
      width="560px"
    >
      <el-form label-width="100px">
        <el-form-item label="名称" required>
          <el-input v-model="providerForm.name" placeholder="如：DeepSeek" maxlength="64" />
        </el-form-item>

        <el-form-item label="快速填充">
          <div class="flex flex-wrap gap-1">
            <el-button
              v-for="preset in presets"
              :key="preset.label"
              size="small"
              @click="providerForm.name = preset.label; applyPreset(preset.base_url)"
            >
              {{ preset.label }}
            </el-button>
          </div>
        </el-form-item>

        <el-form-item label="接口协议">
          <el-radio-group v-model="providerForm.interface_type" :disabled="!!editingProviderId">
            <el-radio value="openai">OpenAI 兼容</el-radio>
            <el-radio value="gemini">Gemini</el-radio>
          </el-radio-group>
        </el-form-item>

        <el-form-item label="Base URL">
          <el-input v-model="providerForm.base_url" placeholder="https://api.deepseek.com/v1" />
          <div class="mt-1 text-xs text-gray-400">
            一般以 /v1 结尾。留空则使用官方默认地址。
          </div>
        </el-form-item>

        <el-form-item label="API Key" :required="!editingProviderId">
          <!-- 已配置且未主动更换时，只显示状态，不给输入框 -->
          <div v-if="editingProviderId && !replacingKey" class="flex w-full items-center gap-2">
            <el-tag type="success" effect="plain">已配置，留空即不修改</el-tag>
            <el-button text type="primary" size="small" @click="replacingKey = true">
              更换密钥
            </el-button>
          </div>
          <div v-else class="w-full">
            <el-input
              v-model="providerForm.api_key"
              type="password"
              show-password
              placeholder="粘贴新的 API Key"
            />
            <div class="mt-1 text-xs text-gray-400">
              密钥经 AES-256-GCM 加密后存储，界面与日志中都不会出现明文。
            </div>
          </div>
        </el-form-item>

        <el-form-item label="启用">
          <el-switch v-model="providerForm.is_active" />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="providerVisible = false">取消</el-button>
        <el-button type="primary" @click="submitProvider">保存</el-button>
      </template>
    </el-dialog>

    <!-- 模型编辑 -->
    <el-dialog
      v-model="modelVisible"
      :title="editingModelId ? '编辑模型' : '添加模型'"
      width="500px"
    >
      <el-form label-width="100px">
        <el-form-item label="供应商" required>
          <el-select v-model="modelForm.provider_id" class="w-full">
            <el-option v-for="p in providers" :key="p.id" :label="p.name" :value="p.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="模型名" required>
          <el-input v-model="modelForm.name" placeholder="如：deepseek-chat" maxlength="128" />
          <div class="mt-1 text-xs text-gray-400">必须是服务商文档里的准确名称。</div>
        </el-form-item>
        <el-form-item label="显示名">
          <el-input v-model="modelForm.display_name" placeholder="选填，如：DeepSeek 对话" />
        </el-form-item>
        <el-form-item label="类型" required>
          <el-radio-group v-model="modelForm.model_type">
            <el-radio value="text">文本</el-radio>
            <el-radio value="vision">视觉</el-radio>
            <el-radio value="embedding">向量</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="最大输出">
          <el-input-number v-model="modelForm.max_tokens" :min="1" :max="2000000" :step="1024" />
          <span class="ml-2 text-xs text-gray-400">tokens</span>
        </el-form-item>
        <el-form-item label="上下文窗口">
          <el-input-number v-model="modelForm.context_window" :min="0" :max="20000000" :step="4096" />
          <span class="ml-2 text-xs text-gray-400">0 表示未知</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="modelForm.is_active" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="modelVisible = false">取消</el-button>
        <el-button type="primary" @click="submitModel">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
