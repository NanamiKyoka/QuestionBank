<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { chatApi, type ChatMessage, type ChatSession } from '@/api/ai'
import { ApiError } from '@/api/http'
import { useSubjectStore } from '@/stores/subject'
import {
  abandonInflightClientTools,
  answerClientToolFrame,
} from '@/composables/useAiClientTools'
import EmptyState from '@/components/ui/EmptyState.vue'
import { streamChatReply, type ChatStreamFrame } from '@/composables/useChatStream'

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

const EXAMPLES = [
  '帮我找 3 道关于函数单调性的题',
  '这份卷子一共几道题？',
  '新建一份「高一月考」试卷',
]

function useExample(text: string) {
  input.value = text
}

let controller: AbortController | null = null
const scrollBox = ref<HTMLElement | null>(null)

const canChat = computed(() => !!subject.currentId)

onMounted(loadSessions)
watch(() => subject.currentId, () => {
  // 会话属于上一个学科：只刷新列表的话 currentSessionId 还指着旧学科的会话，
  // 下一次提问会写进那条会话里。连同在飞的流一起掐掉。
  controller?.abort()
  currentSessionId.value = null
  bubbles.value = []
  void loadSessions()
})

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
 * 发起 SSE 请求并处理事件流。
 *
 * 传输层（fetch + ReadableStream + 帧解析 + done 校验）搬到了
 * composables/useChatStream.ts：组卷编辑器里的对话面板要消费同一条流，
 * 两边各写一份迟早会在协议细节上分叉。
 * 这里只保留"这个页面怎么把帧画出来"。
 */
async function stream(sessionId: number, content: string, assistant: Bubble) {
  await streamChatReply({
    sessionId,
    content,
    scene: scene.value || undefined,
    signal: controller?.signal,
    onFrame: (frame) => {
      handleFrame(frame, assistant)
      // 跟帧滚动，而不是等整条流结束——那和没有流式没区别。
      void scrollToBottom()
    },
  })
}

function handleFrame(frame: ChatStreamFrame, assistant: Bubble) {
  const payload = frame.data
  switch (frame.event) {
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
      // 这个页面没有组卷编辑器，客户端工具一个都执行不了。
      // 但**必须立刻回执**：不回的代价是后端挂满 30 秒超时，
      // 而不是"礼貌地失败"。answerClientToolFrame 负责这条纪律，
      // 工具没注册时它会自己回一句"当前页面没有实现工具 X"。
      assistant.tools.push({ name: String(payload.tool ?? '客户端工具'), running: true })
      answerClientToolFrame(payload)
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

onBeforeUnmount(() => {
  controller?.abort()
  // 离开页面时把还在途的客户端工具票据结清，别让后端白等满 30 秒。
  abandonInflightClientTools('离开对话页')
})

function formatTime(value: string): string {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

</script>

<template>
  <div class="flex h-[calc(100dvh-96px)] gap-3">
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
          :class="currentSessionId === session.id ? 'bg-accent-soft text-accent-text' : 'hover:bg-surface-sunken'"
          @click="openSession(session)"
        >
          <div class="flex items-center gap-1">
            <span class="flex-1 truncate text-sm">{{ session.title }}</span>
            <el-icon
              class="hidden shrink-0 cursor-pointer text-ink-subtle hover:text-danger-text group-hover:block"
              role="button"
              tabindex="0"
              :aria-label="`删除对话「${session.title}」`"
              @click.stop="removeSession(session)"
              @keydown.enter.stop="removeSession(session)"
            >
              <Delete />
            </el-icon>
          </div>
          <div class="text-xs text-ink-subtle">
            {{ session.message_count }} 条 · {{ formatTime(session.updated_at) }}
          </div>
        </div>

        <EmptyState v-if="sessions.length === 0" description="还没有对话" />
      </div>
    </el-card>

    <!-- 对话区 -->
    <el-card shadow="never" class="flex min-w-0 flex-1 flex-col" body-class="flex flex-col h-full">
      <template #header>
        <div class="flex items-center gap-2">
          <h1 class="text-sm font-medium text-ink">AI 助手</h1>
          <el-select v-model="scene" size="small" class="w-40" clearable placeholder="通用模式">
            <el-option label="通用模式" value="" />
            <el-option label="题库场景" value="question_library" />
            <el-option label="组卷场景" value="composition_editor" />
            <el-option label="审核场景" value="import_review" />
          </el-select>
          <span class="text-xs text-ink-subtle">场景决定助手能用哪些工具</span>
          <!-- 组卷场景会解锁三个客户端工具，而它们只认"用户正在编辑的那份卷子"。
               与其让模型反复撞墙，不如在这里把话说清楚。 -->
          <span v-if="scene === 'composition_editor'" class="text-xs text-warning-text">
            改卷请到组卷编辑器里的「AI 助手」，本页没有编辑器，助手无法改动试卷。
          </span>
        </div>
      </template>

      <div ref="scrollBox" v-loading="loadingHistory" class="flex-1 space-y-3 overflow-y-auto pr-1">
        <EmptyState
          v-if="bubbles.length === 0"
          description="问我题库里的任何内容，或者让我帮你组卷"
        >
          <div class="flex flex-wrap justify-center gap-2">
            <button
              v-for="example in EXAMPLES"
              :key="example"
              type="button"
              class="rounded-full border border-hairline px-3 py-1.5 text-xs text-ink-muted transition-colors hover:border-accent-border hover:bg-accent-soft hover:text-accent-text"
              @click="useExample(example)"
            >
              {{ example }}
            </button>
          </div>
        </EmptyState>

        <div
          v-for="(bubble, index) in bubbles"
          :key="index"
          class="flex"
          :class="bubble.role === 'user' ? 'justify-end' : 'justify-start'"
        >
          <div
            class="max-w-[80%] rounded-lg px-3 py-2"
            :class="bubble.role === 'user' ? 'bg-accent text-white' : 'bg-surface-sunken'"
          >
            <!-- 工具调用轨迹 -->
            <div v-if="bubble.tools.length" class="mb-1.5 space-y-1">
              <div
                v-for="(tool, ti) in bubble.tools"
                :key="ti"
                class="flex items-center gap-1 rounded bg-surface/70 px-2 py-1 text-xs text-ink-regular"
              >
                <el-icon v-if="tool.running" class="is-loading"><Loading /></el-icon>
                <el-icon v-else><Select /></el-icon>
                <span>{{ tool.name }}</span>
                <span v-if="tool.output" class="truncate text-ink-subtle">{{ tool.output }}</span>
              </div>
            </div>

            <div v-if="bubble.content" class="whitespace-pre-wrap break-words text-sm">
              {{ bubble.content }}
            </div>

            <div v-if="bubble.streaming && !bubble.content" class="flex items-center gap-1 text-xs text-ink-subtle">
              <el-icon class="is-loading"><Loading /></el-icon>
              思考中…
            </div>

            <div v-if="bubble.error" class="mt-1 text-xs text-danger-text">{{ bubble.error }}</div>
          </div>
        </div>
      </div>

      <div class="mt-3 border-t border-hairline pt-3">
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
          <span class="text-xs text-ink-subtle">
            助手会调用工具查询与修改题库。改动<b>立即生效</b>，不会逐条征求同意；
            删除的题目在回收站里还能捞回来。
          </span>
        </div>
      </div>
    </el-card>
  </div>
</template>
