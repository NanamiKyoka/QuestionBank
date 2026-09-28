// Package service 是业务用例层——HTTP 接口、AI 工具调用、后台任务三条入口
// 共用的唯一业务逻辑出口。
//
// 三条铁律：
//  1. service 里不允许出现 HTTP 概念（gin.Context、状态码）；
//  2. service 里不允许出现 ORM（gorm.DB），只能调用 repository；
//  3. 失败一律返回 *apperr.Error，由 handler / AI 工具适配器各自翻译。
package service

import (
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/repository"
)

// Repos 仓储集合，所有 service 通过它取得数据访问能力。
type Repos struct {
	Users           *repository.UserRepo
	Subjects        *repository.SubjectRepo
	Settings        *repository.SettingRepo
	KnowledgePoints *repository.KnowledgePointRepo
	Tags            *repository.TagRepo
	TagCategories   *repository.TagCategoryRepo
	Questions       *repository.QuestionRepo
	Papers          *repository.PaperRepo
	PaperFolders    *repository.PaperFolderRepo
	AIProviders     *repository.AIProviderRepo
	AIModels        *repository.AIModelRepo
	ImportTasks     *repository.ImportTaskRepo
	Tx              *repository.TxManager
}

// NewRepos 构造仓储集合。
func NewRepos(db *gorm.DB) *Repos {
	return &Repos{
		Users:           repository.NewUserRepo(db),
		Subjects:        repository.NewSubjectRepo(db),
		Settings:        repository.NewSettingRepo(db),
		KnowledgePoints: repository.NewKnowledgePointRepo(db),
		Tags:            repository.NewTagRepo(db),
		TagCategories:   repository.NewTagCategoryRepo(db),
		Questions:       repository.NewQuestionRepo(db),
		Papers:          repository.NewPaperRepo(db),
		PaperFolders:    repository.NewPaperFolderRepo(db),
		AIProviders:     repository.NewAIProviderRepo(db),
		AIModels:        repository.NewAIModelRepo(db),
		ImportTasks:     repository.NewImportTaskRepo(db),
		Tx:              repository.NewTxManager(db),
	}
}
