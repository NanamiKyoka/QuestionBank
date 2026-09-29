/**
 * 对话 SSE 的传输层。
 *
 * 抽出来是因为**两个页面**要消费同一条流：独立的对话页，以及组卷编辑器
 * 内嵌的对话面板。后者是客户端工具能落地的前提——聊天只在一个独立路由上，
 * 就等于组卷工具永远没有执行器。
 *
 * 两条协议纪律（后端 aiagent/sse.go 写死了，前端必须照做）：
 *   1. **所有** 事件的 data 都是 JSON 对象（message 也是），别再多 parse 一次；
 *   2. 必须有终止帧 done，缺帧一律按中断处理，否则界面会一直停在"正在输入"。
 *
 * 另外：写流**之前**的错误（未配置模型、无权限）是普通 JSON 响应，
 * 必须与流内错误分开处理，否则"未配置模型"会被当成流式内容解析成乱码。
 */
import { ApiError } from '@/api/http'

export interface ChatStreamFrame {
  event: string
  data: Record<string, unknown>
}

export interface ChatStreamOptions {
  sessionId: number
  content: string
  /** 场景决定模型能看到哪些工具；组卷编辑器里固定用 composition_editor。 */
  scene?: string
  images?: string[]
  signal?: AbortSignal
  onFrame: (frame: ChatStreamFrame) => void
}

/** 解析一帧 SSE。返回 null 表示这段不是完整帧。 */
export function parseSSEFrame(raw: string): ChatStreamFrame | null {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
  }
  if (!dataLines.length) return null
  try {
    const parsed: unknown = JSON.parse(dataLines.join('\n'))
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    return { event, data: parsed as Record<string, unknown> }
  } catch {
    return null
  }
}

function authHeader(): Record<string, string> {
  const token = localStorage.getItem('qm_token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

/**
 * 发起一次对话并把事件交给调用方。
 *
 * 失败一律**抛**给调用方（ApiError 携带状态码与业务 detail，
 * AbortError 原样透出），由界面决定怎么显示——两个调用点的呈现方式不同，
 * 在这里统一成异常反而要再拆一次。
 */
export async function streamChatReply(options: ChatStreamOptions): Promise<void> {
  const response = await fetch(`/api/v1/chat/sessions/${options.sessionId}/messages`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...authHeader(),
    },
    body: JSON.stringify({
      content: options.content,
      scene: options.scene || undefined,
      images: options.images?.length ? options.images : undefined,
    }),
    signal: options.signal,
  })

  // 写流之前的错误是普通 JSON，不是事件流。
  const contentType = response.headers.get('content-type') ?? ''
  if (!contentType.includes('text/event-stream')) {
    const body = await response.json().catch(() => null)
    if (!response.ok || (body && body.code !== 0)) {
      const envelope = (body ?? {}) as { code?: number; message?: string; detail?: string }
      throw new ApiError(
        response.status,
        envelope.code ?? 0,
        envelope.message ?? `请求失败（${response.status}）`,
        envelope.detail,
      )
    }
    return
  }

  if (!response.body) throw new Error('当前浏览器不支持流式响应')

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let sawDone = false

  const consume = (chunk: string) => {
    const frame = parseSSEFrame(chunk)
    if (!frame) return
    if (frame.event === 'done') sawDone = true
    options.onFrame(frame)
  }

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // SSE 以空行分隔事件；最后一段可能不完整，留到下一轮。
    const chunks = buffer.split('\n\n')
    buffer = chunks.pop() ?? ''
    for (const chunk of chunks) consume(chunk)
  }

  // 处理残留的最后一段。
  if (buffer.trim()) consume(buffer)

  // 没有终止帧说明流被中断了，必须告诉用户，不能让它停在"正在输入"。
  if (!sawDone) {
    throw new Error('连接中断，回答可能不完整')
  }
}
