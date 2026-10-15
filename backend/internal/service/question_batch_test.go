package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

func TestQuestionBatchGetPreservesOrderAndVisibility(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	public := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "公开题")
	private := fx.newQuestion(t, fx.editor, model.VisibilityPrivate, "私有题")

	got, err := fx.qs.BatchGet(ctx, fx.editor, []uint{public.ID, private.ID, public.ID, 0})
	if err != nil {
		t.Fatalf("批量读取失败: %v", err)
	}
	if len(got) != 2 || got[0].ID != public.ID || got[1].ID != private.ID {
		t.Fatalf("批量读取顺序或去重不正确: %+v", got)
	}

	peerGot, err := fx.qs.BatchGet(ctx, fx.peer, []uint{public.ID, private.ID})
	if err != nil {
		t.Fatalf("同科其他用户批量读取失败: %v", err)
	}
	if len(peerGot) != 1 || peerGot[0].ID != public.ID {
		t.Fatalf("不应读到他人私有题: %+v", peerGot)
	}
}
