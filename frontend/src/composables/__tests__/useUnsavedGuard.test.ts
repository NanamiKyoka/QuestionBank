import { describe, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import { useDialogDiscardGuard, useFormDirty } from '../useUnsavedGuard'

// 关闭确认走的是全局 ElMessageBox：没有可注入的实例，只能在模块层换掉。
const confirmMock = vi.hoisted(() => vi.fn())
vi.mock('element-plus', () => ({ ElMessageBox: { confirm: confirmMock } }))

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

describe('useDialogDiscardGuard', () => {
  it('干净的表单直接关，不弹确认', async () => {
    const close = vi.fn()
    const guard = useDialogDiscardGuard(() => false, close, '还有没保存的改动吗')

    await guard.requestClose()

    expect(close).toHaveBeenCalledTimes(1)
    expect(confirmMock).not.toHaveBeenCalled()
  })

  it('脏表单必须先确认，拒绝就留在弹窗里', async () => {
    const close = vi.fn()
    const guard = useDialogDiscardGuard(() => true, close, '这道题还没保存')

    confirmMock.mockRejectedValueOnce(new Error('用户点了留在这里'))
    await guard.requestClose()
    expect(close).not.toHaveBeenCalled()
    expect(confirmMock).toHaveBeenCalledWith('这道题还没保存', '未保存的改动', expect.any(Object))

    confirmMock.mockResolvedValueOnce('confirm')
    await guard.requestClose()
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('beforeClose 只有确认之后才放行 done', async () => {
    const done = vi.fn()
    const guard = useDialogDiscardGuard(() => true, vi.fn(), 'x')

    confirmMock.mockRejectedValueOnce(new Error('用户点了留在这里'))
    guard.beforeClose(done)
    await Promise.resolve()
    await Promise.resolve()
    expect(done).not.toHaveBeenCalled()

    confirmMock.mockResolvedValueOnce('confirm')
    guard.beforeClose(done)
    await Promise.resolve()
    await Promise.resolve()
    expect(done).toHaveBeenCalledTimes(1)
  })
})
