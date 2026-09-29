<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { chatApi, type ChatMessage, type ChatSession } from '@/api/ai'
import { ApiError } from '@/api/http'
import { useSubjectStore } from '@/stores/subject'

/**
 * AI 助手对话。
 *
 * 流式用 fetch + ReadableStream 而不是 EventSource：EventSource 只能发 GET，
 * 既带不了 Authorization 头，也带不了请求体。
 *
 * 后端是两阶段的：写流**之前**的错误走普通 JSON 响应（401/403/503），
 * 写流**之后**的错误走 error 帧。前端必须分别处理，
 * 否则"未配置模型"这类错误会被当成流式内容解析，得到一堆乱码。
 */

interface Bubble {
  role: 'user' | 'assistant'
  content: string
  /** 正在流式输出中。 */
  streaming?: boolean
  /** 工具调用记录，用于展示"助手正在做什么"。 */
  tools: { name: string; output?: string; running: boolean }[]
  error?: string
}

const subject = useSubjectStore()

const sessions = ref<ChatSession[]>([])
const currentSessionId = ref<number | null>(null)
const bubbles = ref<Bubble[]>([])
const input = ref('')
const sending = ref(false)
const loadingHistory = ref(false)
const scene = ref('')

let controller: AbortController | null = null
const scrollBox = ref<HTMLElement | null>(null)

const canChat = computed(() => !!subject.currentId)

onMounted(loadSessions)
watch(() => subject.currentId, loadSessions)

async function loadSessions() {
  if (!subject.currentId) return
  try {
    const page = await chatApi.listSessions(subject.currentId)
    sessions.value = page.list ?? []
  } catch {
    sessions.value = []
  }
}

async function newSession() {
  if (!subject.currentId) return
  const session = await chatApi.createSession({
    subject_id: subject.currentId,
    title: '新对话',
    scene: scene.value || undefined,
  })
  sessions.value.unshift(session)
  currentSessionId.value = session.id
  bubbles.value = []
  return session
}

async function openSession(session: ChatSession) {
  currentSessionId.value = session.id
  loadingHistory.value = true
  bubbles.value = []
  try {
    const messages = await chatApi.listMessages(session.id)
    bubbles.value = messages
      .filter((m: ChatMessage) => m.role === 'user' || m.role === 'assistant')
      .map((m) => ({
        role: m.role as 'user' | 'assistant',
        content: m.content,
        tools: [],
      }))
    await scrollToBottom()
  } finally {
    loadingHistory.value = false
  }
}

async function removeSession(session: ChatSession) {
  try {
    await ElMessageBox.confirm(`确定删除对话「${session.title}」吗？`, '删除确认', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  await chatApi.deleteSession(session.id)
  sessions.value = sessions.value.filter((s) => s.id !== session.id)
  if (currentSessionId.value === session.id) {
    currentSessionId.value = null
    bubbles.value = []
  }
}

async function scrollToBottom() {
  await nextTick()
  const box = scrollBox.value
  if (box) box.scrollTop = box.scrollHeight
}

// ---- 发送 ----

async function send() {
  const content = input.value.trim()
  if (!content || sending.value) return
  if (!canChat.value) {
    ElMessage.warning('请先选择学科')
    return
  }

  let sessionId = currentSessionId.value
  if (!sessionId) {
    const session = await newSession()
    if (!session) return
    sessionId = session.id
  }

  bubbles.value.push({ role: 'user', content, tools: [] })
  input.value = ''
  const assistant: Bubble = { role: 'assistant', content: '', streaming: true, tools: [] }
  bubbles.value.push(assistant)
  await scrollToBottom()

  sending.value = true
  controller = new AbortController()

  try {
    await stream(sessionId, content, assistant)
  } catch (error) {
    if ((error as Error).name === 'AbortError') {
      assistant.error = '已停止生成'
    } else if (error instanceof ApiError) {
      // 写流之前发生的业务错误（未配置模型、无权限……）
      assistant.error = error.detail ? `${error.message}：${error.detail}` : error.message
    } else {
      assistant.error = (error as Error).message || '请求失败'
    }
  } finally {
    assistant.streaming = false
    sending.value = false
    controller = null
    await scrollToBottom()
    void loadSessions()
  }
}

/**
 * 发起 SSE 请求并解析事件流。
 *
 * 用原生 fetch 而不是 axios：axios 在浏览器端拿不到流式的 response.body，
 * 会等整个响应结束才返回，流式就白做了。
 */
async function stream(sessionId: number, content: string, assistant: Bubble) {
  const response = await fetch(`/api/v1/chat/sessions/${sessionId}/messages`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${localStorage.getItem('qm_token') ?? ''}`,
    },
    body: JSON.stringify({ content, scene: scene.value || undefined }),
    signal: controller?.signal,
  })

  // 写流之前的错误是普通 JSON。
  const contentType = response.headers.get('content-type') ?? ''
  if (!contentType.includes('text/event-stream')) {
    const body = await response.json().catch(() => null)
    if (!response.ok || (body && body.code !== 0)) {
      throw new ApiError(
        response.status,
        body?.code ?? 0,
        body?.message ?? `请求失败（${response.status}）`,
        body?.detail,
      )
    }
    return
  }

  if (!response.body) throw new Error('当前浏览器不支持流式响应')

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let sawDone = false

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // SSE 以空行分隔事件；最后一段可能不完整，留到下一轮。
    const chunks = buffer.split('\n\n')
    buffer = chunks.pop() ?? ''

    for (const chunk of chunks) {
      const frame = parseFrame(chunk)
      if (!frame) continue
      if (frame.event === 'done') sawDone = true
      handleFrame(frame.event, frame.data, assistant)
    }
    await scrollToBottom()
  }

  // 处理残留的最后一段。
  if (buffer.trim()) {
    const frame = parseFrame(buffer)
    if (frame) {
      if (frame.event === 'done') sawDone = true
      handleFrame(frame.event, frame.data, assistant)
    }
  }

  // 没有终止帧说明流被中断了，必须告诉用户，不能让它停在"正在输入"。
  if (!sawDone) {
    assistant.error = '连接中断，回答可能不完整'
  }
}

function parseFrame(raw: string): { event: string; data: unknown } | null {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
  }
  if (!dataLines.length) return null
  try {
    return { event, data: JSON.parse(dataLines.join('\n')) }
  } catch {
    return null
  }
}

function handleFrame(event: string, data: unknown, assistant: Bubble) {
  const payload = (data ?? {}) as Record<string, unknown>
  switch (event) {
    case 'message':
      // 后端约定：message 的 data 也是 JSON 对象，不是被引号包起来的字符串。
      assistant.content += String(payload.text ?? '')
      break
    case 'tool_call':
      assistant.tools.push({
        name: String(payload.tool ?? '工具'),
        running: true,
      })
      break
    case 'tool_result': {
      // 找到最近一个同名且仍在运行的工具调用。
      const name = String(payload.tool ?? '')
      for (let i = assistant.tools.length - 1; i >= 0; i--) {
        if (assistant.tools[i].name === name && assistant.tools[i].running) {
          assistant.tools[i].running = false
          assistant.tools[i].output = String(payload.output ?? '完成')
          break
        }
      }
      break
    }
    case 'client_tool':
      // 对话页没有组卷编辑器，无法执行客户端工具。
      // 必须立刻回传一个"失败"，否则后端的运行会一直等到超时。
      void voidRespondClientTool(payload, assistant)
      break
    case 'error':
      assistant.error = String(payload.message ?? '出错了')
      break
    case 'proposal':
      // 候选题目卡片：对话页只做提示，不自动应用。
      assistant.content += `\n（已生成 ${((payload.ids ?? []) as number[]).length} 道候选题目，可到组卷界面查看）`
      break
    case 'usage':
    case 'done':
      break
    default:
      break
  }
}

async function voidRespondClientTool(payload: Record<string, unknown>, assistant: Bubble) {
  const runId = String(payload.run_id ?? '')
  const ticket = String(payload.ticket ?? '')
  const tool = String(payload.tool ?? '')
  if (!runId || !ticket) return
  assistant.tools.push({ name: tool, running: false, output: '当前页面无法执行' })
  try {
    await chatApi.resolveClientTool(runId, {
      ticket,
      ok: false,
      content: `当前页面无法执行 ${tool}。请在对应的编辑页面里让我执行，或用文字说明。`,
    })
  } catch {
    // 回传失败说明运行已经结束了，无需再处理。
  }
}

function stop() {
  controller?.abort()
}

async function onKeydown(event: KeyboardEvent) {
  // Enter 发送，Shift+Enter 换行——这是聊天框的通用约定。
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
    event.preventDefault()
    await send()
  }
}

onBeforeUnmount(() => controller?.abort())

function formatTime(value: string): string {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

</script>

<template>
  <div class="flex h-[calc(100vh-140px)] gap-3">
    <!-- 会话列表 -->
    <el-card shadow="never" class="w-60 shrink-0">
      <template #header>
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium">对话</span>
          <el-button text size="small" @click="newSession">
            <el-icon><Plus /></el-icon>
          </el-button>
        </div>
      </template>

      <div class="max-h-full overflow-y-auto">
        <div
          v-for="session in sessions"
          :key="session.id"
          class="group mb-1 cursor-pointer rounded px-2 py-1.5"
          :class="currentSessionId === session.id ? 'bg-blue-50 text-blue-600' : 'hover:bg-gray-50'"
          @click="openSession(session)"
        >
          <div class="flex items-center gap-1">
            <span class="flex-1 truncate text-sm">{{ session.title }}</span>
            <el-icon
              class="hidden text-gray-400 hover:text-red-500 group-hover:block"
              @click.stop="removeSession(session)"
            >
              <Delete />
            </el-icon>
          </div>
          <div class="text-xs text-gray-400">
            {{ session.message_count }} 条 · {{ formatTime(session.updated_at) }}
          </div>
        </div>

        <el-empty v-if="sessions.length === 0" description="还没有对话" :image-size="48" />
      </div>
    </el-card>

    <!-- 对话区 -->
    <el-card shadow="never" class="flex min-w-0 flex-1 flex-col" body-class="flex flex-col h-full">
      <template #header>
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium">AI 助手</span>
          <el-select v-model="scene" size="small" class="w-40" clearable placeholder="通用模式">
            <el-option label="通用模式" value="" />
            <el-option label="题库场景" value="question_library" />
            <el-option label="组卷场景" value="composition_editor" />
            <el-option label="审核场景" value="import_review" />
          </el-select>
          <span class="text-xs text-gray-400">场景决定助手能用哪些工具</span>
        </div>
      </template>

      <div ref="scrollBox" v-loading="loadingHistory" class="flex-1 space-y-3 overflow-y-auto pr-1">
        <el-empty
          v-if="bubbles.length === 0"
          description="问我题库里的任何内容，或者让我帮你组卷"
        >
          <div class="space-y-1 text-left text-xs text-gray-500">
            <div>· 帮我找 3 道关于函数单调性的题</div>
            <div>· 这份卷子一共几道题？</div>
            <div>· 新建一份「高一月考」试卷</div>
          </div>
        </el-empty>

        <div
          v-for="(bubble, index) in bubbles"
          :key="index"
          class="flex"
          :class="bubble.role === 'user' ? 'justify-end' : 'justify-start'"
        >
          <div
            class="max-w-[80%] rounded-lg px-3 py-2"
            :class="bubble.role === 'user' ? 'bg-blue-600 text-white' : 'bg-gray-100'"
          >
            <!-- 工具调用轨迹 -->
            <div v-if="bubble.tools.length" class="mb-1.5 space-y-1">
              <div
                v-for="(tool, ti) in bubble.tools"
                :key="ti"
                class="flex items-center gap-1 rounded bg-white/70 px-2 py-1 text-xs text-gray-600"
              >
                <el-icon v-if="tool.running" class="is-loading"><Loading /></el-icon>
                <el-icon v-else><Select /></el-icon>
                <span>{{ tool.name }}</span>
                <span v-if="tool.output" class="truncate text-gray-400">{{ tool.output }}</span>
              </div>
            </div>

            <div v-if="bubble.content" class="whitespace-pre-wrap break-words text-sm">
              {{ bubble.content }}
            </div>

            <div v-if="bubble.streaming && !bubble.content" class="flex items-center gap-1 text-xs text-gray-400">
              <el-icon class="is-loading"><Loading /></el-icon>
              思考中…
            </div>

            <div v-if="bubble.error" class="mt-1 text-xs text-red-500">{{ bubble.error }}</div>
          </div>
        </div>
      </div>

      <div class="mt-3 border-t border-gray-200 pt-3">
        <el-input
          v-model="input"
          type="textarea"
          :rows="3"
          resize="none"
          placeholder="输入问题，Enter 发送，Shift+Enter 换行"
          :disabled="sending"
          @keydown="onKeydown"
        />
        <div class="mt-2 flex items-center gap-2">
          <el-button v-if="sending" type="danger" plain @click="stop">停止生成</el-button>
          <el-button v-else type="primary" :disabled="!input.trim()" @click="send">发送</el-button>
          <span class="text-xs text-gray-400">
            助手会调用工具查询题库；涉及修改的操作会先向你确认。
          </span>
        </div>
      </div>
    </el-card>
  </div>
</template>
