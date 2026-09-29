import { get, put } from './http'

/** 取值形态，决定界面用什么控件编辑。与后端 service.SettingType 一致。 */
export type SettingType = 'bool' | 'int' | 'text'

/**
 * 单个系统设置项。
 *
 * `editable` 与 `note` 是这个接口的重点：后端只把"确实有代码会读"的键
 * 标成可写，其余一律只读，并在 note 里写清为什么改了不起作用。
 * 界面必须照着这两字段呈现——一个能保存却没人读的输入框比没有更糟。
 */
export interface SystemSettingItem {
  key: string
  label: string
  description: string
  type: SettingType
  /** 当前生效的取值；数据库里没有这一行时等于 def。 */
  value: string
  /** 默认值，用于"未配置"提示。 */
  def: string
  editable: boolean
  /** 这个键当前到底生不生效，写给管理员看的一句话。 */
  note: string
  /** 数据库里是否真的有这一行。false 表示界面显示的是默认值。 */
  saved: boolean
  updated_by: number
  /** 最后修改时间；从未配置过时为 null。 */
  updated_at: string | null
}

export const settingsApi = {
  /** GET /settings —— 仅全局管理员。返回目录内的键 + 库里未登记的键。 */
  list: () => get<SystemSettingItem[]>('/settings'),

  /**
   * PUT /settings/:key —— 仅全局管理员，且只接受后端标成 editable 的键。
   *
   * 布尔值接受 true/false/1/0/yes/no/on/off，落库统一为 "true"/"false"，
   * 与仓储 SettingRepo.GetBool 的识别集合保持一致。
   */
  update: (key: string, value: string) =>
    put<SystemSettingItem>(`/settings/${encodeURIComponent(key)}`, { value }),
}
