import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { onBeforeRouteLeave } from 'vue-router'

/**
 * 未保存改动的拦截。
 *
 * 两种"离开"要分别接上：站内路由可以弹确认框，把代价说清楚再放行；
 * 关标签页与刷新只能触发浏览器原生确认（必须调用 preventDefault，
 * 文案则由浏览器自己定）。此前全站只有组卷台记得做这件事，
 * 其余表单一律裸奔——刷新一下，输入就没了，还没有人告诉用户会这样。
 *
 * `isDirty` 用 getter 而不是 ref：脏不脏可能由多个来源合成
 * （资料表单 + 密码框，全局模板 + 学科覆盖）。
 */
export function useUnsavedGuard(
  isDirty: () => boolean,
  message = '当前有未保存的改动，确定离开吗？',
): void {
  onBeforeRouteLeave(async () => {
    if (!isDirty()) return true
    try {
      await ElMessageBox.confirm(message, '未保存的改动', {
        type: 'warning',
        confirmButtonText: '离开并丢弃',
        cancelButtonText: '留在这里',
      })
      return true
    } catch {
      return false
    }
  })

  function onBeforeUnload(event: BeforeUnloadEvent) {
    if (!isDirty()) return
    event.preventDefault()
    // 现代浏览器忽略自定义文案，但必须调用才能触发原生确认。
    event.returnValue = ''
  }

  onMounted(() => window.addEventListener('beforeunload', onBeforeUnload))
  onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))
}

/**
 * 以初始快照为基准的脏标记。
 *
 * 表单页面几乎都是"一份草稿 + 一个保存按钮"：逐字段手写 dirty 既啰嗦又
 * 必然漏掉某一个，而漏掉的那一个字段就是"改了却不提示"。这里对整份值做
 * 稳定序列化后比较——恢复到原值同样算干净，这正是用户的预期。
 *
 * `markClean()` 要在**基准被刷新的时机**显式调用：加载回来的值、保存后的
 * 值都是新的原点，不重置的话页面一打开就带着一肚子"未保存改动"。
 */
export function useFormDirty<T>(read: () => T): { dirty: Ref<boolean>; markClean: () => void } {
  const dirty = ref(false)
  let baseline = stableStringify(read())

  watch(
    read,
    (value) => {
      dirty.value = stableStringify(value) !== baseline
    },
    { deep: true },
  )

  function markClean() {
    baseline = stableStringify(read())
    dirty.value = false
  }

  return { dirty, markClean }
}

/**
 * 弹窗关闭前的未保存确认。
 *
 * 弹窗是全站最容易手滑丢稿的地方：点 X、按 ESC、点遮罩都会直接关掉，
 * 而题干、模型配置这类内容是一个字一个字敲出来的——关闭前必须说一声。
 *
 * 返回两个处理器：`requestClose` 给「取消」按钮用，`beforeClose` 给
 * el-dialog 的 `:before-close`（X / ESC / 遮罩都走它），两条路同一道确认。
 */
export function useDialogDiscardGuard(
  isDirty: () => boolean,
  close: () => void,
  message = '表单里还有没保存的内容，确定关闭吗？',
) {
  async function confirmed(): Promise<boolean> {
    if (!isDirty()) return true
    try {
      await ElMessageBox.confirm(message, '未保存的改动', {
        type: 'warning',
        confirmButtonText: '关闭并丢弃',
        cancelButtonText: '留在这里',
      })
      return true
    } catch {
      return false
    }
  }

  return {
    requestClose: async () => {
      if (await confirmed()) close()
    },
    beforeClose: (done: () => void) => {
      void confirmed().then((ok) => {
        if (ok) done()
      })
    },
  }
}

/**
 * 键序无关的 JSON 序列化。
 *
 * 直接 JSON.stringify 的话，后端返回的字段顺序一变就会被当成"改过"——
 * 这种误报会让用户学会无视拦截框。
 */
function stableStringify(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? 'null'
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`

  const entries = Object.entries(value as Record<string, unknown>)
    .filter(([, v]) => v !== undefined)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return `{${entries.map(([k, v]) => `${JSON.stringify(k)}:${stableStringify(v)}`).join(',')}}`
}
