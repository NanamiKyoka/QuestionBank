/**
 * AI 运行的收尾语义：**为什么停**、**花了多少**。
 *
 * 后端把三重预算（轮次 / 工具调用 / 墙钟）编码成 done 帧里的 stop_reason
 * 与 budget（backend/internal/aiagent/events.go、sse.go），usage 帧则逐轮上报
 * token。但 stop_reason 是一串英文枚举，墙钟超时时气泡甚至是空的——用户
 * 只看到一片沉默。两个对话页面（独立对话页、组卷台内嵌面板）都要把它们
 * 翻译成人话，各写一份迟早在措辞上分叉。
 *
 * 这里全是纯函数：不改入参、不碰响应式、不认识 Vue。
 */

/** 一次运行的预算上限，随 done 帧下发。 */
export interface RunBudget {
  max_turns?: number
  max_tool_calls?: number
  wall_clock_seconds?: number
}

/** 累计用量。usage 帧是每一轮的增量，多轮要相加。 */
export interface RunUsage {
  prompt: number
  completion: number
}

/**
 * done 帧 → 气泡下方的一行收尾说明。
 *
 * 返回 null 表示不必说话：正常收尾没有信息量，失败已经由 error 帧上屏，
 * 取消也由停止按钮那一侧交代，重复提示只会变成噪音。
 */
export function stopNotice(frame: Record<string, unknown>): string | null {
  const reason = typeof frame.stop_reason === 'string' ? frame.stop_reason : ''
  const budget = readBudget(frame.budget)
  switch (reason) {
    case 'time_limit':
      return budget?.wall_clock_seconds
        ? `已达 ${formatDuration(budget.wall_clock_seconds)} 时间预算，助手被迫停止`
        : '已达时间预算，助手被迫停止'
    case 'budget_exhausted':
      return budget?.max_turns
        ? `已达 ${budget.max_turns} 轮对话上限，助手被迫停止`
        : '已达轮次上限，助手被迫停止'
    case 'tool_limit':
      return budget?.max_tool_calls
        ? `已达 ${budget.max_tool_calls} 次工具调用上限，助手被迫停止`
        : '已达工具调用上限，助手被迫停止'
    case 'canceled':
      return '已停止生成'
    default:
      return null
  }
}

/** usage 帧累加进已有计数，首帧传 undefined。 */
export function addUsage(current: RunUsage | undefined, frame: Record<string, unknown>): RunUsage {
  return {
    prompt: (current?.prompt ?? 0) + toCount(frame.prompt_tokens),
    completion: (current?.completion ?? 0) + toCount(frame.completion_tokens),
  }
}

/** 气泡下方的一行用量说明；一轮 token 都没有就别占那一行。 */
export function usageNote(usage: RunUsage | undefined): string | null {
  if (!usage || usage.prompt + usage.completion <= 0) return null
  return `输入 ${formatTokens(usage.prompt)} · 输出 ${formatTokens(usage.completion)} tokens`
}

/** SSE 帧的 data 是 JSON 对象，字段形状不可信，逐个挑能用的。 */
function readBudget(value: unknown): RunBudget | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  return {
    max_turns: toCount(raw.max_turns),
    max_tool_calls: toCount(raw.max_tool_calls),
    wall_clock_seconds: toCount(raw.wall_clock_seconds),
  }
}

function toCount(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0
}

function formatDuration(seconds: number): string {
  if (seconds >= 60 && seconds % 60 === 0) return `${seconds / 60} 分钟`
  if (seconds >= 60) return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`
  return `${seconds} 秒`
}

function formatTokens(count: number): string {
  if (count < 1000) return String(count)
  if (count < 10000) return `${(count / 1000).toFixed(1)}k`
  return `${Math.round(count / 1000)}k`
}
