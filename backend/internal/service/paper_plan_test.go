package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

func TestPlanQuestionsBalancesDifficultyAndExcludesIDs(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	q1 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度一")
	q2 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度三")
	q3 := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "难度五")
	for id, difficulty := range map[uint]int{q1.ID: 1, q2.ID: 3, q3.ID: 5} {
		if err := fx.repos.Questions.UpdateFields(ctx, id, map[string]any{
			"difficulty": difficulty,
			"status":     model.QuestionStatusPublished,
		}); err != nil {
			t.Fatalf("更新题目失败: %v", err)
		}
	}

	plan, err := fx.qs.PlanQuestions(ctx, fx.editor, fx.subject.ID, &schema.PaperPlanRequest{
		Count:         3,
		QuestionType:  model.QuestionTypeTrueFalse,
		DifficultyMin: 1,
		DifficultyMax: 5,
		Seed:          1,
	})
	if err != nil {
		t.Fatalf("生成预选失败: %v", err)
	}
	if plan.Selected != 3 || plan.Missing != 0 {
		t.Fatalf("预选数量不正确: %+v", plan)
	}
	if plan.DifficultyCounts[1] != 1 || plan.DifficultyCounts[3] != 1 || plan.DifficultyCounts[5] != 1 {
		t.Fatalf("难度分布未均衡: %+v", plan.DifficultyCounts)
	}

	plan, err = fx.qs.PlanQuestions(ctx, fx.editor, fx.subject.ID, &schema.PaperPlanRequest{
		Count:              3,
		QuestionType:       model.QuestionTypeTrueFalse,
		DifficultyMin:      1,
		DifficultyMax:      5,
		ExcludeQuestionIDs: []uint{q2.ID},
		Seed:               1,
	})
	if err != nil {
		t.Fatalf("生成排除预选失败: %v", err)
	}
	if plan.Selected != 2 || plan.Missing != 1 || len(plan.Warnings) == 0 {
		t.Fatalf("排除条件未正确反馈缺口: %+v", plan)
	}
	for _, item := range plan.Items {
		if item.ID == q2.ID {
			t.Fatal("排除题目仍被选中")
		}
	}
}
