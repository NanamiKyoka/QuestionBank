import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import type { QuestionBrief, QuestionType, RichDoc } from '@/api/types'

/**
 * 试题篮。
 *
 * 老师组卷的真实节奏是"边翻题库边攒题"：翻几页、挑几道、换个筛选再挑几道，
 * 最后一次性倒进某份卷子。把这份暂存状态放在列表页的 `selection` 里等于没有——
 * 一翻页、一跳转就没了。所以这里做成全局 store + localStorage，
 * 跨页面、跨学科、跨刷新都留着。
 *
 * 只存**展示用的轻量字段**。真正入卷时服务端会按 question_id 重新读取题目，
 * 这里的内容永远只是给人看的预览，因此不需要（也不应该）跟着题库一起更新。
 */
export interface BasketItem {
  id: number
  subject_id: number
  /** 题干纯文本预览。 */
  stem_text: string
  /**
   * 题干富文本。篮子是给人挑题用的，公式按源码显示（\frac{1}{2}）
   * 会让整栏都读不下去，所以预览必须能排版。
   * 老版本存进 localStorage 的条目没有这个字段，按 stem_text 回退。
   */
  stem_doc?: RichDoc | null
  question_type: QuestionType
  question_type_label: string
  difficulty: number
}

const STORAGE_KEY = 'qm_question_basket'

/** 由题库列表行构造篮内条目。 */
export function toBasketItem(row: QuestionBrief): BasketItem {
  return {
    id: row.id,
    subject_id: row.subject_id,
    stem_text: row.stem_text || '',
    stem_doc: row.stem_doc ?? null,
    question_type: row.question_type,
    question_type_label: row.question_type_label,
    difficulty: row.difficulty,
  }
}

function readStorage(): BasketItem[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    // 逐条校验而不是整个丢弃：一条脏数据不该让整个篮清零。
    return parsed.filter((it): it is BasketItem => {
      if (!it || typeof it !== 'object') return false
      const item = it as Partial<BasketItem>
      return typeof item.id === 'number' && typeof item.subject_id === 'number'
    })
  } catch {
    // 存了坏 JSON 就丢掉重来：篮子是便利功能，不值得因此报错。
    localStorage.removeItem(STORAGE_KEY)
    return []
  }
}

export const useBasketStore = defineStore('basket', () => {
  const items = ref<BasketItem[]>(readStorage())

  // 任何一次改动都落盘。用 watcher 而不是每次改动手动调 persist：
  // 后者漏一次就是"关掉页面少一道题"，而且不报错，很难发现。
  watch(
    items,
    (value) => {
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(value))
      } catch {
        // 配额写满或隐私模式禁写：篮子退化成会话内有效，不阻断使用。
      }
    },
    { deep: true },
  )

  const count = computed(() => items.value.length)

  function has(id: number): boolean {
    return items.value.some((i) => i.id === id)
  }

  function add(item: BasketItem): boolean {
    if (has(item.id)) return false
    items.value = [...items.value, item]
    return true
  }

  /** 批量加入，返回真正新加进去的条数（已存在的会被静默跳过）。 */
  function addMany(list: BasketItem[]): number {
    const existing = new Set(items.value.map((i) => i.id))
    const fresh = list.filter((i) => !existing.has(i.id))
    if (fresh.length === 0) return 0
    items.value = [...items.value, ...fresh]
    return fresh.length
  }

  function remove(id: number) {
    items.value = items.value.filter((i) => i.id !== id)
  }

  function removeMany(ids: number[]) {
    const set = new Set(ids)
    items.value = items.value.filter((i) => !set.has(i.id))
  }

  /** 有则移出、没有则加入。列表行上的篮子按钮用它。 */
  function toggle(item: BasketItem): boolean {
    if (has(item.id)) {
      remove(item.id)
      return false
    }
    add(item)
    return true
  }

  /** 传学科只清该学科分组，不传则清空整个篮子。 */
  function clear(subjectId?: number) {
    items.value = subjectId == null ? [] : items.value.filter((i) => i.subject_id !== subjectId)
  }

  /** 按学科分组。插入顺序保持题库里的挑题顺序，不重排。 */
  const groupedBySubject = computed(() => {
    const map = new Map<number, BasketItem[]>()
    for (const item of items.value) {
      const list = map.get(item.subject_id)
      if (list) list.push(item)
      else map.set(item.subject_id, [item])
    }
    return map
  })

  return {
    items,
    count,
    groupedBySubject,
    has,
    add,
    addMany,
    remove,
    removeMany,
    toggle,
    clear,
  }
})
