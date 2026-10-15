package schema

import "github.com/NanamiKyoka/questionmind/internal/model"

type PaperBlueprintSection struct {
	Key               string             `json:"key" binding:"omitempty,max=64"`
	Title             string             `json:"title" binding:"omitempty,max=100"`
	Count             int                `json:"count" binding:"required,min=1,max=100"`
	QuestionType      model.QuestionType `json:"question_type" binding:"omitempty"`
	Score             float64            `json:"score" binding:"omitempty,min=0,max=1000"`
	DifficultyMin     int                `json:"difficulty_min" binding:"omitempty,min=1,max=5"`
	DifficultyMax     int                `json:"difficulty_max" binding:"omitempty,min=1,max=5"`
	DifficultyCounts  map[int]int        `json:"difficulty_counts"`
	KnowledgePointIDs []uint             `json:"knowledge_point_ids"`
	IncludeSub        bool               `json:"include_sub_knowledge"`
	TagIDs            []uint             `json:"tag_ids"`
	Source            string             `json:"source" binding:"omitempty,max=255"`
}

type PaperBlueprintRequest struct {
	Sections           []PaperBlueprintSection `json:"sections" binding:"required,min=1,max=10,dive"`
	ExcludeQuestionIDs []uint                  `json:"exclude_question_ids"`
	Seed               int64                   `json:"seed"`
}

type PaperBlueprintItem struct {
	QuestionBrief
	SectionKey   string  `json:"section_key"`
	SectionTitle string  `json:"section_title"`
	Score        float64 `json:"score"`
	Reason       string  `json:"reason"`
}

type PaperBlueprintSectionResult struct {
	Key              string      `json:"key"`
	Title            string      `json:"title"`
	Requested        int         `json:"requested"`
	Selected         int         `json:"selected"`
	Missing          int         `json:"missing"`
	DifficultyCounts map[int]int `json:"difficulty_counts"`
}

type PaperBlueprintResponse struct {
	Requested int                           `json:"requested"`
	Selected  int                           `json:"selected"`
	Missing   int                           `json:"missing"`
	Items     []PaperBlueprintItem          `json:"items"`
	Sections  []PaperBlueprintSectionResult `json:"sections"`
	Warnings  []string                      `json:"warnings"`
}
