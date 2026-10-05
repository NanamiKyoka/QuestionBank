// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useBasketStore, type BasketItem } from '@/stores/basket'

const STORAGE_KEY = 'qm_question_basket'

function item(id: number, subjectId: number): BasketItem {
  return {
    id,
    subject_id: subjectId,
    stem_text: `第 ${id} 题`,
    stem_doc: null,
    question_type: 'single_choice',
    question_type_label: '单选题',
    difficulty: 3,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
})

describe('试题篮', () => {
  it('加入、去重与移出', () => {
    const basket = useBasketStore()

    expect(basket.add(item(1, 1))).toBe(true)
    expect(basket.add(item(1, 1))).toBe(false)
    expect(basket.count).toBe(1)

    expect(basket.toggle(item(2, 1))).toBe(true)
    expect(basket.toggle(item(2, 1))).toBe(false)
    expect(basket.count).toBe(1)

    basket.remove(1)
    expect(basket.count).toBe(0)
  })

  it('按学科分组，清空只清指定学科——不同学科不互相串', () => {
    const basket = useBasketStore()
    basket.add(item(1, 1))
    basket.add(item(2, 1))
    basket.add(item(3, 2))

    expect([...basket.groupedBySubject.keys()]).toEqual([1, 2])
    expect(basket.groupedBySubject.get(1)).toHaveLength(2)

    basket.clear(1)

    expect(basket.count).toBe(1)
    expect(basket.items[0].id).toBe(3)

    basket.clear()
    expect(basket.count).toBe(0)
  })

  it('改动落盘，重开页面（新 store 实例）读得回来', async () => {
    const basket = useBasketStore()
    basket.add(item(7, 1))
    await nextTick()

    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]')).toHaveLength(1)

    setActivePinia(createPinia())
    expect(useBasketStore().items).toHaveLength(1)
  })

  it('一条脏数据只丢那一条，不清零整篮', () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify([item(1, 1), { stem_text: '没有 id 的坏条目' }, null, item(3, 2)]),
    )

    const basket = useBasketStore()

    expect(basket.items.map((it) => it.id)).toEqual([1, 3])
  })

  it('存了坏 JSON 就丢掉重来，而不是带着异常启动', () => {
    localStorage.setItem(STORAGE_KEY, '{ 这不是 json')

    const basket = useBasketStore()

    expect(basket.count).toBe(0)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
  })
})
