import { describe, expect, it } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import { useFormDirty } from '../useUnsavedGuard'

describe('useFormDirty', () => {
  it('改动标脏，恢复原值又回到干净', async () => {
    const form = reactive({ name: '张三', tags: ['a'] })
    const { dirty } = useFormDirty(() => form)
    await nextTick()
    expect(dirty.value).toBe(false)

    form.name = '李四'
    await nextTick()
    expect(dirty.value).toBe(true)

    form.name = '张三'
    await nextTick()
    expect(dirty.value).toBe(false)
  })

  it('markClean 把当前值定为新的原点', async () => {
    const drafts = ref<Record<string, string>>({ a: '1' })
    const { dirty, markClean } = useFormDirty(() => drafts.value)

    drafts.value = { a: '2' }
    await nextTick()
    expect(dirty.value).toBe(true)

    markClean()
    expect(dirty.value).toBe(false)

    drafts.value = { a: '3' }
    await nextTick()
    expect(dirty.value).toBe(true)
  })

  it('内容相同、键序不同不算改动', async () => {
    const source = ref<Record<string, string>>({ a: '1', b: '2' })
    const { dirty, markClean } = useFormDirty(() => source.value)
    markClean()

    source.value = { b: '2', a: '1' }
    await nextTick()
    expect(dirty.value).toBe(false)

    source.value = { b: '3', a: '1' }
    await nextTick()
    expect(dirty.value).toBe(true)
  })

  it('数组增删同样算改动', async () => {
    const list = ref<string[]>(['a', 'b'])
    const { dirty } = useFormDirty(() => list.value)
    await nextTick()
    expect(dirty.value).toBe(false)

    list.value.push('c')
    await nextTick()
    expect(dirty.value).toBe(true)
  })
})
