package model

// AllModels 返回需要建表的全部模型，供 AutoMigrate 与迁移校验使用。
//
// 顺序有讲究：被引用的表要排在引用方之前（如 users 先于 subject_members），
// 否则部分数据库会在建外键时失败。
func AllModels() []any {
	return []any{
		// 基础
		&User{},
		&Subject{},
		&SubjectMember{},

		// 题库
		&KnowledgePoint{},
		&TagCategory{},
		&Tag{},
		&Question{},
		&QuestionTag{},
		&QuestionKnowledgePoint{},
		&ReviewLog{},

		// 组卷
		&PaperFolder{},
		&Paper{},
		&PaperNode{},
		&PaperVersion{},
		&PaperEvent{},

		// AI
		&AIProvider{},
		&AIModel{},
		&VectorDoc{},
		&ChatSession{},
		&ChatMessage{},
		&AgentRun{},
		&AgentStep{},
		&PromptTemplate{},

		// 系统
		&SystemSetting{},
		&ActivityLog{},
		&ImportTask{},
	}
}
