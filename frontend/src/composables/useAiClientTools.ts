/**
 * 客户端工具的票据生命周期。
 *
 * 协议（后端 aiagent/channel.go）：
 *   服务端推一帧 `client_tool`（run_id + ticket + tool + input），
 *   **同时把整个 SSE 生成器阻塞**在这个票据上，最多等 30 秒。
 *   前端执行完把结果 POST 回 `/chat/runs/:run_id/tool-result` 才唤醒它。
 *
 * 因此本文件唯一不可让步的规则是：**每一张票据都要有回执**。
 * 漏掉任何一张（未知工具、用户取消、处理器抛异常、页面正在卸载），
 * 后端都会白白挂满 30 秒，用户看到的是"点了没反应然后模型道歉"。
 * 参考实现（reference/.../useAiClientTools.ts）也是同一条纪律：
 * run() 永远 resolve 成一个结果对象，调用方只管回传。
 *
 * SECURITY：这里没有任何"跳转/读文件"一类的工具，也不接受模型给的路径。
 * 注册表里的处理器只认自己那几把结构化的操作，模型给的字符串永远不会被
 * 拼进 router.push 或文件系统路径——智能录题会把外部文档喂给模型，
 * 那正是提示注入的载体。
 */
import { http, type ApiEnvelope } from '@/api/http'

/** 一次执行的结论。ok=false 时 content 是给模型看的失败原因。 */
export interface ClientToolResult {
  ok: boolean
  content?: string
  data?: Record<string, unknown>
}

export type ClientToolHandler = (
  args: Record<string, unknown>,
) => Promise<ClientToolResult> | ClientToolResult

/**
 * 一帧 `client_tool` 的负载。
 *
 * 字段一律声明成 unknown：SSE 的 data 是 JSON，不是编译期契约，
 * 页面上的值随时可能变成 null 或少字段。取值统一走下面的窄化辅助函数。
 */
export interface ClientToolFrame {
  run_id?: unknown
  tool_call_id?: unknown
  ticket?: unknown
  tool?: unknown
  input?: unknown
}

/**
 * 处理器自身的执行预算。
 *
 * 必须**小于**后端的 30 秒票据超时：处理器多半是在等网络（拉题目详情），
 * 一旦它卡住，我们宁可提前交一份"还在处理"的回执，也不能让后端先超时——
 * 先超时的话，这张票据就再也送不进去了（回传只会得到 404）。
 */
const EXECUTION_DEADLINE_MS = 20_000

/** 回执的网络重试次数（仅对网络抖动，票据过期重试无意义）。 */
const POST_ATTEMPTS = 2

// ---- 注册表 ----

/**
 * 模块级，而不是每个页面自己存一份。
 *
 * SSE 的消费端住在聊天面板里，而处理器注册在组卷编辑器里——两者必须是
 * 同一个注册表，否则编辑器注册完，面板根本找不到它。
 */
const registry = new Map<string, ClientToolHandler>()

/** 注册一个客户端工具。页面挂载时注册、卸载时注销。 */
export function registerClientTool(name: string, handler: ClientToolHandler): void {
  registry.set(name, handler)
}

/** 注销。忘记注销的后果是助手去改一块用户已经看不见的画布。 */
export function unregisterClientTool(name: string): void {
  registry.delete(name)
}

/** 已注册的工具名，供自检与调试。 */
export function registeredClientTools(): string[] {
  return [...registry.keys()].sort()
}

// ---- 执行 ----

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}

function asText(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function describeError(error: unknown): string {
  if (error instanceof Error && error.message) return error.message
  return String(error)
}

/**
 * 执行一张票据并保证有回执。
 *
 * 三条出口都必须产出结果：工具没注册、处理器抛错、超过执行预算。
 */
export async function runClientTool(
  name: string,
  args: Record<string, unknown>,
): Promise<ClientToolResult> {
  const handler = registry.get(name)
  // 没注册是正常结局而不是 bug：用户可能已经离开页面，或者后端先于前端上线了。
  // 立刻失败并说清楚，比让运行挂满 30 秒有用得多。
  if (!handler) {
    return {
      ok: false,
      content:
        `当前页面没有实现工具 ${name || '(空)'}。` +
        '组卷相关工具只能在组卷编辑器里执行，请请用户在编辑器页面的对话框中操作。',
    }
  }

  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    const execution = Promise.resolve()
      .then(() => handler(args))
      .then(
        (result): ClientToolResult => result,
        (error: unknown): ClientToolResult => ({
          ok: false,
          content: `前端执行 ${name} 失败：${describeError(error)}`,
        }),
      )

    // 竞一个预算出来：处理器真的卡住时，先交回执再让它在后台跑完，
    // 至少不会把后端拖到超时（超时后回执就再也送不进去了）。
    const deadline = new Promise<ClientToolResult>((resolve) => {
      timer = setTimeout(
        () =>
          resolve({
            ok: false,
            content: `${name} 在前端仍未处理完（已超过 ${EXECUTION_DEADLINE_MS / 1000} 秒），本次没有回执。`,
          }),
        EXECUTION_DEADLINE_MS,
      )
    })

    return await Promise.race([execution, deadline])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

// ---- 回执 ----

/**
 * 正在处理中的票据。
 *
 * 记下来是为了两件事：同一张票据不重复回传（后端的一次性语义），
 * 以及页面卸载时能知道还有哪些没交回执。
 */
const inflight = new Map<string, { runId: string; ticket: string; tool: string }>()

function ticketKey(runId: string, ticket: string): string {
  return `${runId}:${ticket}`
}

/** 当前在途的票据数，供诊断与测试。 */
export function inflightClientTools(): number {
  return inflight.size
}

/**
 * 把结果送回后端。
 *
 * 走裸 http 而不是 chatApi：票据过期时后端会回 40401，
 * 那是**正常结局**（运行自己超时或结束了），走全局错误分支会给用户弹一个
 * 莫名其妙的红条。所以除 401 外一律当作"已送达"处理，自己看信封里的 code。
 * 401 仍然放行——凭证失效要走统一的登出流程，不能在这里吞掉。
 */
async function postResult(
  runId: string,
  ticket: string,
  result: ClientToolResult,
): Promise<void> {
  // run_id 拼进 URL：即便它由服务端生成（UUID），也按不可信输入编码。
  const url = `/chat/runs/${encodeURIComponent(runId)}/tool-result`
  const body = {
    ticket,
    ok: result.ok,
    content: result.content ?? (result.ok ? '已完成。' : '执行失败。'),
    data: result.data,
  }

  for (let attempt = 1; attempt <= POST_ATTEMPTS; attempt++) {
    try {
      const response = await http.post(url, body, { validateStatus: (status) => status !== 401 })
      const envelope = response.data as ApiEnvelope<unknown>
      // code=0 表示这一张票据真的被兑现了；非 0（已过期/已兑现/多副本部署）
      // 是服务端明确拒收，重试只会再得到同一个错误。
      if (envelope.code !== 0) return
      return
    } catch {
      // 压根没连上（网络抖动、连接被重置）才值得再试一次；
      // 兜底还有后端自己的 30 秒超时，不会把运行永久挂死。
      if (attempt >= POST_ATTEMPTS) return
    }
  }
}

/**
 * 处理一帧 `client_tool`：执行 → 回传，永不回传不出去。
 *
 * 调用方**不要** await 这个函数来处理界面状态，它会顺着 UI 一起跑；
 * 这里的异常已经在内部吃干净了。
 */
export function answerClientToolFrame(frame: ClientToolFrame): void {
  const runId = asText(frame.run_id)
  const ticket = asText(frame.ticket)
  // 没有票据就没有可回执的对象，只能放弃（后端会在 30 秒后自行收场）。
  if (!runId || !ticket) return

  const key = ticketKey(runId, ticket)
  if (inflight.has(key)) return
  inflight.set(key, { runId, ticket, tool: asText(frame.tool) })

  void (async () => {
    try {
      const result = await runClientTool(asText(frame.tool), asRecord(frame.input))
      await postResult(runId, ticket, result)
    } finally {
      inflight.delete(key)
    }
  })()
}

/**
 * 页面即将卸载时，把所有在途票据按"用户已离开"结清。
 *
 * 正常情况下处理器会自己跑完并回执；这一步是给"处理器仍在 await 一个
 * 永远不会完成的网络请求"兜底的。重复回执无害——后端的一次性语义靠
 * sync.Once 保证，第二张只会拿到 404。
 */
export function abandonInflightClientTools(reason: string): void {
  for (const [key, item] of [...inflight.entries()]) {
    inflight.delete(key)
    void postResult(item.runId, item.ticket, {
      ok: false,
      content: `前端已离开页面（${reason}），${item.tool || '该工具'} 未执行。`,
    })
  }
}
