import { authApi } from './index'

/**
 * 个人资料相关的接口。
 *
 * 请求体形状对齐后端 schema/auth.go:113-146：三个端点早就存在
 * （router.go:172-174），只是前端一直没有入口。这里只补类型与命名，
 * 实际调用仍走 authApi——同一个端点出现两个实现，
 * 过几个月就会有人改了一个、忘了另一个。
 */

/**
 * PATCH /auth/me 的请求体。
 *
 * 三个字段都是可选，后端用指针区分"不传"与"传空串"：
 * 不传 = 不改这一项，传空串 = 真的清空。密码与用户名不在其中。
 */
export interface ProfileUpdatePayload {
  nickname?: string
  email?: string
  avatar?: string
}

/** POST /auth/me/password 的请求体。后端要求新密码 8–72 位。 */
export interface ChangePasswordPayload {
  current_password: string
  /** 8–72 位；72 是 bcrypt 的上限，再长会被静默截断，因此后端显式拒绝。 */
  new_password: string
}

export const profileApi = {
  /** PATCH /auth/me —— 返回更新后的用户，前端应同步进 auth store。 */
  updateMe: (payload: ProfileUpdatePayload) => authApi.updateMe(payload),

  /** POST /auth/me/password —— 无返回值；改密后旧 token 仍有效到自然过期。 */
  changePassword: (payload: ChangePasswordPayload) =>
    authApi.changePassword(payload.current_password, payload.new_password),

  /** POST /auth/me/last-subject —— 让后端记住上次登录的学科。 */
  setLastSubject: (subjectId: number) => authApi.setLastSubject(subjectId),

  /** GET /auth/me —— 刷新页面后拿完整资料（含 memberships）。 */
  me: () => authApi.me(),
}
