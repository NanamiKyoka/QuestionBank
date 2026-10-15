package schema

import "github.com/NanamiKyoka/questionmind/internal/model"

type PaperPlanRequest struct {
	Count              int                `json:"count" binding:"required,min=1,max=100"`
	QuestionType       model.QuestionType `json:"question_type" binding:"omitempty"`
	DifficultyMin      int                `json:"difficulty_min" binding:"omitempty,min=1,max=5"`
	DifficultyMax      int                `json:"difficulty_max" binding:"omitempty,min=1,max=5"`
	KnowledgePointIDs  []uint             `json:"knowledge_point_ids"`
	IncludeSub         bool               `json:"include_sub_knowledge"`
	TagIDs             []uint             `json:"tag_ids"`
	Source             string             `json:"source" binding:"omitempty,max=255"`
	ExcludeQuestionIDs []uint             `json:"exclude_question_ids"`
	Seed               int64              `json:"seed"`
}

type PaperPlanResponse struct {
	Requested        int             `json:"requested"`
	Available        int             `json:"available"`
	Selected         int             `json:"selected"`
	Missing          int             `json:"missing"`
	Items            []QuestionBrief `json:"items"`
	DifficultyCounts map[int]int     `json:"difficulty_counts"`
	Warnings         []string        `json:"warnings"`
}
