package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

func TestPreviewBlueprintSelectsSectionsAndScores(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	q1 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度一 A")
	q2 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度三")
	q3 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度五")
	q4 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度一 B")
	for id, difficulty := range map[uint]int{q1.ID: 1, q2.ID: 3, q3.ID: 5, q4.ID: 1} {
		if err := fx.repos.Questions.UpdateFields(ctx, id, map[string]any{
			"difficulty": difficulty,
			"status":     model.QuestionStatusPublished,
		}); err != nil {
			t.Fatalf("更新题目失败: %v", err)
		}
	}

	result, err := fx.qs.PreviewBlueprint(ctx, fx.editor, fx.subject.ID, &schema.PaperBlueprintRequest{
		Sections: []schema.PaperBlueprintSection{
			{
				Key:              "choice",
				Title:            "基础选择",
				Count:            3,
				QuestionType:     model.QuestionTypeTrueFalse,
				Score:            2,
				DifficultyMin:    1,
				DifficultyMax:    5,
				DifficultyCounts: map[int]int{1: 1, 3: 1, 5: 1},
			},
			{
				Key:           "warmup",
				Title:         "入门判断",
				Count:         1,
				QuestionType:  model.QuestionTypeTrueFalse,
				Score:         1,
				DifficultyMin: 1,
				DifficultyMax: 1,
			},
		},
		Seed: 1,
	})
	if err != nil {
		t.Fatalf("生成命题蓝图失败: %v", err)
	}
	if result.Selected != 4 || result.Missing != 0 {
		t.Fatalf("蓝图数量不正确: %+v", result)
	}
	seen := make(map[uint]struct{}, len(result.Items))
	for _, item := range result.Items {
		if _, exists := seen[item.ID]; exists {
			t.Fatalf("蓝图题目重复: %d", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.SectionKey != "choice" && item.SectionKey != "warmup" {
			t.Fatalf("分组 key 不正确: %+v", item)
		}
		if item.Score != 2 && item.Score != 1 {
			t.Fatalf("分值未带入: %+v", item)
		}
	}

	result, err = fx.qs.PreviewBlueprint(ctx, fx.editor, fx.subject.ID, &schema.PaperBlueprintRequest{
		Sections: []schema.PaperBlueprintSection{{
			Title:         "受限分组",
			Count:         2,
			DifficultyMin: 5,
			DifficultyMax: 5,
		}},
		ExcludeQuestionIDs: []uint{q3.ID},
		Seed:               1,
	})
	if err != nil {
		t.Fatalf("生成带排除条件的蓝图失败: %v", err)
	}
	if result.Selected != 0 || result.Missing != 2 || len(result.Warnings) == 0 {
		t.Fatalf("缺口没有正确反馈: %+v", result)
	}
}
