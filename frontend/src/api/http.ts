import axios, { AxiosError, type AxiosInstance, type AxiosRequestConfig } from 'axios'
import { ElMessage } from 'element-plus'

/**
 * 后端统一响应体：{ code, message, detail, data }
 * code 为 0 表示成功；失败时 code 是形如 40401 的业务错误码。
 */
export interface ApiEnvelope<T> {
  code: number
  message: string
  detail?: string
  data?: T
}

export interface PageResult<T> {
  list: T[]
  total: number
  page: number
  page_size: number
}

/** 领域错误：把后端的 code/message/detail 打包带走，供业务层判断。 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: number,
    message: string,
    readonly detail?: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }

  /** 是否为并发冲突（乐观锁 revision 不匹配、目录非空等）。 */
  get isConflict() {
    return this.status === 409
  }
}

/**
 * 未登录处理器。
 *
 * 用注册回调而不是直接 import router：router 会 import store，
 * store 会 import api，api 再 import router 就构成循环依赖。
 * 让 main.ts 在路由就绪后注册，依赖方向保持单向。
 */
type Handler = () => void
let onUnauthorized: Handler | null = null
let onSetupRequired: Handler | null = null

export function setAuthFailureHandlers(handlers: {
  onUnauthorized?: Handler
  onSetupRequired?: Handler
}) {
  onUnauthorized = handlers.onUnauthorized ?? null
  onSetupRequired = handlers.onSetupRequired ?? null
}

let accessToken: string | null = null

export function setAccessToken(token: string | null) {
  accessToken = token
}

/** 由业务层决定哪些错误自己处理（不弹全局提示）。 */
const silentCodes = new Set<number>()

export function silenceError(code: number) {
  silentCodes.add(code)
}

export const http: AxiosInstance = axios.create({
  baseURL: '/api/v1',
  timeout: 120_000, // 智能录题与 AI 对话耗时较长
})

http.interceptors.request.use((config) => {
  if (accessToken) {
    config.headers.Authorization = `Bearer ${accessToken}`
  }
  return config
})

http.interceptors.response.use(
  (response) => response,
  (error: AxiosError<ApiEnvelope<unknown>>) => {
    // 网络层失败：没有响应体，只能给通用提示。
    if (!error.response) {
      if (error.code === 'ECONNABORTED') {
        ElMessage.error('请求超时，请稍后重试')
      } else {
        ElMessage.error('网络异常，请检查后端服务是否已启动')
      }
      return Promise.reject(new ApiError(0, 0, '网络异常'))
    }

    const { status, data } = error.response
    const code = data?.code ?? 0
    const message = data?.message ?? '请求失败'
    const detail = data?.detail

    // 后端约定：503 + code 50301 表示尚未完成初始化。
    if (status === 503 && code === 50301) {
      onSetupRequired?.()
      return Promise.reject(new ApiError(status, code, message, detail))
    }

    // 401 = 未登录/凭证失效；前端要清 token 并跳登录。
    if (status === 401) {
      onUnauthorized?.()
      return Promise.reject(new ApiError(status, code, message, detail))
    }

    if (!silentCodes.has(code) && status !== 409) {
      // 409 交给业务层处理（比如提示"数据已被他人修改"并刷新），
      // 其它错误统一弹提示，避免每个调用点都写一遍。
      ElMessage.error(detail ? `${message}：${detail}` : message)
    }

    return Promise.reject(new ApiError(status, code, message, detail))
  },
)

/** 拆包：成功时返回 data，失败时抛 ApiError。 */
async function unwrap<T>(
  promise: Promise<{ data: ApiEnvelope<T>; status: number }>,
): Promise<T> {
  const response = await promise
  const body = response.data
  if (body.code !== 0) {
    throw new ApiError(response.status, body.code, body.message, body.detail)
  }
  return body.data as T
}

export function get<T>(url: string, params?: Record<string, unknown>): Promise<T> {
  return unwrap<T>(http.get(url, { params: cleanParams(params) }))
}

export function post<T>(url: string, body?: unknown, config?: AxiosRequestConfig): Promise<T> {
  return unwrap<T>(http.post(url, body, config))
}

export function put<T>(url: string, body?: unknown): Promise<T> {
  return unwrap<T>(http.put(url, body))
}

export function patch<T>(url: string, body?: unknown): Promise<T> {
  return unwrap<T>(http.patch(url, body))
}

export function del<T>(url: string, params?: Record<string, unknown>): Promise<T> {
  return unwrap<T>(http.delete(url, { params: cleanParams(params) }))
}

/**
 * 去掉 undefined / 空字符串的查询参数。
 *
 * 保留 0 与 false：它们是有意义的取值（如 page=0 表示取全部、
 * success=false 表示只看失败记录），当成"未填"会改变语义。
 */
function cleanParams(params?: Record<string, unknown>): Record<string, unknown> | undefined {
  if (!params) return undefined
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    out[key] = value
  }
  return out
}

/** 下载二进制附件（导出 Word / LaTeX）。 */
export async function downloadFile(
  url: string,
  params: Record<string, unknown> | undefined,
  fallbackName: string,
): Promise<void> {
  const response = await http.get(url, { params: cleanParams(params), responseType: 'blob' })

  // 从 Content-Disposition 里取文件名；后端同时给了 ASCII 与 UTF-8 两种形式。
  const disposition = response.headers['content-disposition'] as string | undefined
  let filename = fallbackName
  if (disposition) {
    const utf8 = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
    if (utf8?.[1]) {
      filename = decodeURIComponent(utf8[1])
    } else {
      const ascii = /filename="([^"]+)"/i.exec(disposition)
      if (ascii?.[1]) filename = ascii[1]
    }
  }

  const blobUrl = URL.createObjectURL(response.data as Blob)
  const link = document.createElement('a')
  link.href = blobUrl
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  // 释放对象 URL，否则大文件会一直占着内存。
  URL.revokeObjectURL(blobUrl)
}
