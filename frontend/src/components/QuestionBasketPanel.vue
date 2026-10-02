<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import CompositionTargetPicker from '@/components/CompositionTargetPicker.vue'
import RichContent from '@/components/RichContent.vue'
import { useBasketStore, type BasketItem } from '@/stores/basket'
import { useSubjectStore } from '@/stores/subject'
import DifficultyStars from '@/components/ui/DifficultyStars.vue'
import type { AppendQuestionsResult } from '@/api/paper'

/**
 * 试题篮面板。
 *
 * 按学科分组，因为一道题只能进**同一学科**的试卷——跨学科引用会被后端
 * 拒绝（checkQuestionRefs）。先分好组，"加入试卷"就永远只在一个学科里选，
 * 不会选到一半才发现选不进去。
 */
const open = defineModel<boolean>({ default: false })

const emit = defineEmits<{
  /** 追加成功。带上有多少道、进了哪份卷子，方便调用方提示或跳转。 */
  added: [result: AppendQuestionsResult]
}>()

const basket = useBasketStore()
const subject = useSubjectStore()

/** 目标选择器当前服务的学科分组。 */
const pickerSubjectId = ref<number | null>(null)
const pickerQuestionIds = ref<number[]>([])
const pickerVisible = ref(false)

function subjectName(id: number): string {
  return subject.subjects.find((s) => s.id === id)?.name ?? `学科 ${id}`
}

function openPickerFor(subjectId: number, items: BasketItem[]) {
  pickerSubjectId.value = subjectId
  pickerQuestionIds.value = items.map((i) => i.id)
  pickerVisible.value = true
}

/** 入卷成功后才把这批题从篮子里拿走：失败时题还得留着重试。 */
function onAdded(result: AppendQuestionsResult) {
  basket.removeMany(pickerQuestionIds.value)
  emit('added', result)
}

async function clearGroup(subjectId: number) {
  const n = basket.groupedBySubject.get(subjectId)?.length ?? 0
  try {
    await ElMessageBox.confirm(`确定清空「${subjectName(subjectId)}」分组的 ${n} 道题吗？`, '清空分组', {
      type: 'warning',
      confirmButtonText: '清空',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  basket.clear(subjectId)
  ElMessage.success('已清空该分组')
}

async function clearAll() {
  try {
    await ElMessageBox.confirm(`确定清空试题篮里的 ${basket.count} 道题吗？`, '清空试题篮', {
      type: 'warning',
      confirmButtonText: '清空',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  basket.clear()
  ElMessage.success('试题篮已清空')
}
</script>

<template>
  <el-drawer v-model="open" title="试题篮" size="420px">
    <div class="mb-3 flex items-center justify-between">
      <span class="text-xs text-ink-subtle">
        共 {{ basket.count }} 道 · 暂存在本地浏览器，刷新不丢
      </span>
      <el-button
        v-if="basket.count > 0"
        text
        size="small"
        type="danger"
        @click="clearAll"
      >
        <el-icon class="mr-1"><Delete /></el-icon>清空
      </el-button>
    </div>

    <el-empty v-if="basket.count === 0" description="试题篮是空的。在题库里挑几道题加进来。" :image-size="72" />

    <div v-else>
      <div v-for="[sid, list] in basket.groupedBySubject" :key="sid" class="mb-5 last:mb-0">
        <div class="sticky top-0 z-10 flex items-center justify-between bg-surface py-2">
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium">{{ subjectName(sid) }}</span>
            <span class="text-xs text-ink-subtle">{{ list.length }} 题</span>
          </div>
          <div class="flex items-center gap-1">
            <el-button size="small" type="primary" plain @click="openPickerFor(sid, list)">
              <el-icon class="mr-1"><Document /></el-icon>加入试卷
            </el-button>
            <el-button text size="small" @click="clearGroup(sid)">清空本组</el-button>
          </div>
        </div>

        <div
          v-for="item in list"
          :key="item.id"
          class="group mb-1 flex items-start gap-2 rounded border border-hairline p-2"
        >
          <div class="min-w-0 flex-1">
            <div class="mb-0.5 flex items-center gap-1.5 text-xs text-ink-subtle">
              <el-tag size="small" effect="plain">{{ item.question_type_label }}</el-tag>
              <DifficultyStars :level="item.difficulty" />
            </div>
            <RichContent v-if="item.stem_doc" :doc="item.stem_doc" compact class="stem-clamp text-sm" />
            <div v-else class="stem-clamp text-sm">{{ item.stem_text || '（无题干文本）' }}</div>
          </div>
          <el-button text size="small" type="danger" @click="basket.remove(item.id)">
            <el-icon><Close /></el-icon>
          </el-button>
        </div>
      </div>
    </div>

    <CompositionTargetPicker
      v-model="pickerVisible"
      :subject-id="pickerSubjectId"
      :question-ids="pickerQuestionIds"
      title="加入试卷"
      @added="onAdded"
    />
  </el-drawer>
</template>
