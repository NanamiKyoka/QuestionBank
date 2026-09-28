package service

import (
	"github.com/google/uuid"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// newUUID 生成节点 id。
//
// 文档节点的 id 由客户端生成并作为主键，服务端只在"派生新节点"时
// （如新增解析条目、复制试卷）才需要自己造 id。
func newUUID() string { return uuid.NewString() }

// authorize 数据级鉴权。
//
// 这是"第二道门"：HTTP 中间件的 RequirePermission 只覆盖 HTTP 入口，
// 而 service 还会被 AI 工具调用与后台任务复用——那些入口没有中间件。
// 因此每一次 service 调用都必须自己判权，不能依赖调用方已经判过。
func authorize(user *model.User, perm model.Permission, subjectID uint) error {
	if user == nil {
		return apperr.ErrUnauthorized
	}
	if !user.Can(perm, subjectID) {
		return apperr.ErrForbidden
	}
	return nil
}

// authorizeRead 读操作：能看见该学科即可。
func authorizeRead(user *model.User, subjectID uint) error {
	return authorize(user, model.PermViewQuestion, subjectID)
}

// authorizeSuperuser 仅全局管理员。
func authorizeSuperuser(user *model.User) error {
	if user == nil {
		return apperr.ErrUnauthorized
	}
	if !user.IsSuperuser {
		return apperr.ErrForbidden
	}
	return nil
}
