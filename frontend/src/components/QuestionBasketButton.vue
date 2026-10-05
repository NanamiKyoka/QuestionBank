<script setup lang="ts">
import { ref } from 'vue'
import QuestionBasketPanel from '@/components/QuestionBasketPanel.vue'
import { useBasketStore } from '@/stores/basket'
import type { AppendQuestionsResult } from '@/api/paper'

/**
 * 试题篮悬浮入口。
 *
 * 自带面板，一次挂载就能用：放在布局里（而不是每个列表页），
 * 老师在任何页面都能把攒下来的题倒进卷子。篮子是空的时整个按钮不渲染，
 * 免得在题库页挡住表格、在试卷页挡住工具栏。
 */
withDefaults(
  defineProps<{
    /** 距右边缘的距离，覆盖默认的 right-6。 */
    offsetRight?: string
  }>(),
  { offsetRight: '1.5rem' },
)

const emit = defineEmits<{
  added: [result: AppendQuestionsResult]
}>()

const basket = useBasketStore()
const visible = ref(false)
</script>

<template>
  <el-badge
    v-if="basket.count > 0"
    :value="basket.count"
    :max="99"
    class="basket-entry"
    :style="{ right: offsetRight }"
  >
    <el-button
      circle
      class="!h-12 !w-12 shadow-lg"
      title="试题篮"
      aria-label="试题篮"
      @click="visible = true"
    >
      <el-icon class="!text-xl"><ShoppingCart /></el-icon>
    </el-button>
  </el-badge>

  <QuestionBasketPanel v-model="visible" @added="emit('added', $event)" />
</template>

<style scoped>
/* 固定在右下角：列表页滚到底时它还在，选题的手感不被打断。 */
.basket-entry {
  position: fixed;
  bottom: 5rem;
  z-index: 40;
}
</style>
