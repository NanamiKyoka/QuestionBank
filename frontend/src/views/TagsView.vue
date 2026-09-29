<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { tagApi } from '@/api'
import type { Tag, TagCategory } from '@/api/types'
import { useSubjectStore } from '@/stores/subject'

const subject = useSubjectStore()

const loading = ref(false)
const tags = ref<Tag[]>([])
const categories = ref<TagCategory[]>([])
const activeCategory = ref<number | 'all' | 'none'>('all')

const tagDialog = ref(false)
const editingTagId = ref<number | null>(null)
const tagForm = reactive({ name: '', color: '#409EFF', category_id: null as number | null })

const categoryDialog = ref(false)
const categoryName = ref('')

const canManage = computed(() => subject.can('manage_subject'))

/** 预设色板：让标签颜色整体协调，而不是每个都手调十六进制值。 */
const palette = [
  '#409EFF', '#67C23A', '#E6A23C', '#F56C6C', '#909399',
  '#8E44AD', '#16A085', '#D35400', '#2C3E50', '#C0392B',
]

onMounted(load)
watch(() => subject.currentId, load)

async function load() {
  if (!subject.currentId) return
  loading.value = true
  try {
    const [tagList, categoryList] = await Promise.all([
      tagApi.list(subject.currentId),
      tagApi.categories(subject.currentId),
    ])
    tags.value = tagList
    categories.value = categoryList
  } finally {
    loading.value = false
  }
}

const filtered = computed(() => {
  if (activeCategory.value === 'all') return tags.value
  if (activeCategory.value === 'none') return tags.value.filter((t) => !t.category_id)
  return tags.value.filter((t) => t.category_id === activeCategory.value)
})

function openCreateTag() {
  editingTagId.value = null
  tagForm.name = ''
  tagForm.color = palette[tags.value.length % palette.length] ?? '#409EFF'
  tagForm.category_id =
    typeof activeCategory.value === 'number' ? activeCategory.value : null
  tagDialog.value = true
}

function openEditTag(tag: Tag) {
  editingTagId.value = tag.id
  tagForm.name = tag.name
  tagForm.color = tag.color || '#409EFF'
  tagForm.category_id = tag.category_id
  tagDialog.value = true
}

async function submitTag() {
  if (!subject.currentId) return
  if (!tagForm.name.trim()) {
    ElMessage.warning('标签名不能为空')
    return
  }
  if (editingTagId.value) {
    await tagApi.update(editingTagId.value, {
      name: tagForm.name.trim(),
      color: tagForm.color,
      // 后端用 clear_category 显式表达"清空分类"，
      // 因为 category_id 为 0 与"没传"在 JSON 里无法区分。
      ...(tagForm.category_id === null
        ? { clear_category: true }
        : { category_id: tagForm.category_id }),
    })
    ElMessage.success('已保存')
  } else {
    await tagApi.create(subject.currentId, {
      name: tagForm.name.trim(),
      color: tagForm.color,
      category_id: tagForm.category_id,
    })
    ElMessage.success('已创建')
  }
  tagDialog.value = false
  await load()
}

async function removeTag(tag: Tag) {
  try {
    await ElMessageBox.confirm(`确定删除标签「${tag.name}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  await tagApi.remove(tag.id)
  ElMessage.success('已删除')
  await load()
}

async function submitCategory() {
  if (!subject.currentId || !categoryName.value.trim()) {
    ElMessage.warning('分类名不能为空')
    return
  }
  await tagApi.createCategory(subject.currentId, categoryName.value.trim())
  ElMessage.success('已创建')
  categoryDialog.value = false
  categoryName.value = ''
  await load()
}

async function removeCategory(category: TagCategory) {
  try {
    await ElMessageBox.confirm(
      `确定删除分类「${category.name}」吗？分类下的标签不会被删除，只是不再归类。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await tagApi.removeCategory(category.id)
  ElMessage.success('已删除')
  activeCategory.value = 'all'
  await load()
}
</script>

<template>
  <div class="flex gap-3">
    <!-- 分类侧栏 -->
    <el-card shadow="never" class="w-56 shrink-0">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">分类</span>
          <el-button v-if="canManage" text size="small" @click="categoryDialog = true">
            <el-icon><Plus /></el-icon>
          </el-button>
        </div>
      </template>

      <div
        class="cursor-pointer rounded px-2 py-1.5 text-sm"
        :class="activeCategory === 'all' ? 'bg-blue-50 text-blue-600' : 'hover:bg-gray-50'"
        @click="activeCategory = 'all'"
      >
        全部标签
        <span class="float-right text-xs text-gray-400">{{ tags.length }}</span>
      </div>

      <div
        v-for="category in categories"
        :key="category.id"
        class="group flex cursor-pointer items-center rounded px-2 py-1.5 text-sm"
        :class="activeCategory === category.id ? 'bg-blue-50 text-blue-600' : 'hover:bg-gray-50'"
        @click="activeCategory = category.id"
      >
        <span class="flex-1 truncate">{{ category.name }}</span>
        <span class="text-xs text-gray-400">{{ category.tag_count }}</span>
        <el-icon
          v-if="canManage"
          class="ml-1 hidden text-red-400 group-hover:block"
          @click.stop="removeCategory(category)"
        >
          <Delete />
        </el-icon>
      </div>

      <div
        class="cursor-pointer rounded px-2 py-1.5 text-sm"
        :class="activeCategory === 'none' ? 'bg-blue-50 text-blue-600' : 'hover:bg-gray-50'"
        @click="activeCategory = 'none'"
      >
        未分类
        <span class="float-right text-xs text-gray-400">
          {{ tags.filter((t) => !t.category_id).length }}
        </span>
      </div>
    </el-card>

    <!-- 标签列表 -->
    <el-card shadow="never" class="flex-1">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">标签</span>
          <el-button v-if="canManage" type="primary" size="small" @click="openCreateTag">
            <el-icon class="mr-1"><Plus /></el-icon>新建标签
          </el-button>
        </div>
      </template>

      <div v-loading="loading" class="flex flex-wrap gap-2">
        <div
          v-for="tag in filtered"
          :key="tag.id"
          class="group flex items-center gap-2 rounded-full border border-gray-200 py-1 pl-3 pr-2"
        >
          <span class="h-2.5 w-2.5 rounded-full" :style="{ backgroundColor: tag.color || '#909399' }" />
          <span class="text-sm">{{ tag.name }}</span>
          <span class="text-xs text-gray-400">{{ tag.question_count }}</span>
          <template v-if="canManage">
            <el-icon class="cursor-pointer text-gray-400 hover:text-blue-500" @click="openEditTag(tag)">
              <Edit />
            </el-icon>
            <el-icon class="cursor-pointer text-gray-400 hover:text-red-500" @click="removeTag(tag)">
              <Close />
            </el-icon>
          </template>
        </div>

        <el-empty
          v-if="!loading && filtered.length === 0"
          :description="subject.currentId ? '该分类下还没有标签' : '请先选择学科'"
        />
      </div>
    </el-card>

    <!-- 标签编辑 -->
    <el-dialog v-model="tagDialog" :title="editingTagId ? '编辑标签' : '新建标签'" width="420px">
      <el-form label-width="70px">
        <el-form-item label="名称" required>
          <el-input v-model="tagForm.name" maxlength="50" placeholder="如：易错题" />
        </el-form-item>
        <el-form-item label="分类">
          <el-select v-model="tagForm.category_id" clearable placeholder="不分类" class="w-full">
            <el-option
              v-for="category in categories"
              :key="category.id"
              :label="category.name"
              :value="category.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="颜色">
          <div class="flex flex-wrap gap-2">
            <div
              v-for="color in palette"
              :key="color"
              class="h-6 w-6 cursor-pointer rounded border-2"
              :style="{
                backgroundColor: color,
                borderColor: tagForm.color === color ? '#303133' : 'transparent',
              }"
              @click="tagForm.color = color"
            />
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="tagDialog = false">取消</el-button>
        <el-button type="primary" @click="submitTag">保存</el-button>
      </template>
    </el-dialog>

    <!-- 新建分类 -->
    <el-dialog v-model="categoryDialog" title="新建分类" width="360px">
      <el-input v-model="categoryName" placeholder="如：能力层级" maxlength="50" @keyup.enter="submitCategory" />
      <template #footer>
        <el-button @click="categoryDialog = false">取消</el-button>
        <el-button type="primary" @click="submitCategory">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>
