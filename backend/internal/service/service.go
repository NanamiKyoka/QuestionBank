// Package service 是业务用例层——HTTP 接口、AI 工具调用、后台任务三条入口
// 共用的唯一业务逻辑出口。
//
// 三条铁律：
//  1. service 里不允许出现 HTTP 概念（gin.Context、状态码）；
//  2. service 里不允许出现 ORM（gorm.DB），只能调用 repository；
//  3. 失败一律返回 *apperr.Error，由 handler / AI 工具适配器各自翻译。
//
// 这样做的好处是"AI 帮用户改一道题"和"用户在界面上改一道题"走的是
// 完全相同的校验与权限逻辑，不会出现两套行为、两套 bug。
package service

import (
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/repository"
)

// Repos 仓储集合。所有 service 通过它取得数据访问能力。
type Repos struct {
	Users    *repository.UserRepo
	Subjects *repository.SubjectRepo
	Settings *repository.SettingRepo
	Tx       *repository.TxManager
}

// NewRepos 构造仓储集合。
func NewRepos(db *gorm.DB) *Repos {
	return &Repos{
		Users:    repository.NewUserRepo(db),
		Subjects: repository.NewSubjectRepo(db),
		Settings: repository.NewSettingRepo(db),
		Tx:       repository.NewTxManager(db),
	}
}
