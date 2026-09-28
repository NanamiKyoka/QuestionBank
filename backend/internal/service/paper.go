package service

import (
	"context"
	"fmt"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// ListPapersQuery 试卷列表参数。
type ListPapersQuery struct {
	Scope    model.PaperScope
	FolderID *uint
	Unfiled  bool
	Keyword  string
	Status   model.PaperStatus
}

// ListPapers 分页查询试卷。
func (s *PaperService) ListPapers(ctx context.Context, user *model.User, subjectID uint, q ListPapersQuery, limit, offset int) ([]schema.PaperBrief, int64, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, 0, err
	}

	filter := repository.PaperFilter{
		SubjectID: subjectID,
		Scope:     q.Scope,
		FolderID:  q.FolderID,
		Unfiled:   q.Unfiled,
		Keyword:   q.Keyword,
		Status:    q.Status,
	}
	// 个人空间只能看自己的卷子；共享空间人人可见。
	if q.Scope == model.PaperScopePersonal {
		owner := user.ID
		filter.OwnerID = &owner
	} else if q.Scope == "" {
		// 未指定空间时，个人卷只列出自己的，共享卷全部列出。
		filter.OwnerID = &user.ID
	}

	list, total, err := s.repos.Papers.List(ctx, filter, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}
	if len(list) == 0 {
		return []schema.PaperBrief{}, total, nil
	}

	ids := make([]uint, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	counts, err := s.repos.Papers.QuestionCountsByPaper(ctx, ids)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.PaperBrief, 0, len(list))
	for i := range list {
		brief := schema.NewPaperBrief(&list[i])
		brief.QuestionCount = counts[brief.ID]
		out = append(out, brief)
	}
	return out, total, nil
}

// CreatePaper 新建试卷。
func (s *PaperService) CreatePaper(ctx context.Context, user *model.User, subjectID uint, req *schema.PaperCreateRequest) (*schema.PaperDetail, error) {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}
	scope, ownerID, err := s.resolveScope(user, req.Scope)
	if err != nil {
		return nil, err
	}

	if req.FolderID != nil {
		folder, err := s.repos.PaperFolders.Get(ctx, *req.FolderID)
		if err != nil {
			return nil, err
		}
		if folder.SubjectID != subjectID || !sameSpace(folder, scope, ownerID) {
			return nil, apperr.New(apperr.CodeCrossSubject, "目录不在同一个空间下")
		}
	}

	displayJSON, _ := domain.NormalizeQuestionDisplay(nil)

	paper := &model.Paper{
		SubjectID:        subjectID,
		Scope:            scope,
		OwnerID:          ownerID,
		FolderID:         req.FolderID,
		Title:            req.Title,
		Description:      req.Description,
		Status:           model.PaperStatusDraft,
		NumberingEnabled: true,
		ScoringEnabled:   false,
		QuestionDisplay:  displayJSON,
		Revision:         1,
		CreatedBy:        user.ID,
		UpdatedBy:        user.ID,
	}
	if req.NumberingEnabled != nil {
		paper.NumberingEnabled = *req.NumberingEnabled
	}
	if req.ScoringEnabled != nil {
		paper.ScoringEnabled = *req.ScoringEnabled
	}

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Papers.Create(ctx, paper); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   paper.ID,
			EventType: "created",
			ActorID:   user.ID,
			Summary:   fmt.Sprintf("创建了试卷《%s》", paper.Title),
		})
	})
	if err != nil {
		return nil, err
	}

	return s.GetPaper(ctx, user, paper.ID)
}

// GetPaper 试卷详情：元信息 + 节点树 + 题目内容投影。
func (s *PaperService) GetPaper(ctx context.Context, user *model.User, id uint) (*schema.PaperDetail, error) {
	paper, err := s.repos.Papers.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}
	// 他人个人空间的卷子不可见，返回 404 防止枚举。
	if paper.Scope == model.PaperScopePersonal &&
		(paper.OwnerID == nil || *paper.OwnerID != user.ID) && !user.IsSuperuser {
		return nil, apperr.ErrNotFound
	}

	nodes, err := s.repos.Papers.Nodes(ctx, id)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	detail := &schema.PaperDetail{
		PaperBrief: schema.NewPaperBrief(paper),
		Nodes:      make([]schema.PaperNodeResponse, 0, len(nodes)),
		Questions:  map[uint]schema.PaperQuestionProjection{},
		Permissions: schema.PaperPermissions{
			CanEdit:     s.canWrite(user, paper),
			CanDelete:   s.canDelete(user, paper),
			CanFinalize: s.canWrite(user, paper),
		},
	}
	var display model.QuestionDisplay
	if !paper.QuestionDisplay.IsEmpty() {
		_ = paper.QuestionDisplay.Into(&display)
	}
	detail.QuestionDisplay = display

	for i := range nodes {
		detail.Nodes = append(detail.Nodes, schema.NewPaperNodeResponse(&nodes[i]))
	}
	detail.QuestionCount = len(domain.CollectQuestionIDs(nodes))

	projections, err := s.projectQuestions(ctx, nodes)
	if err != nil {
		return nil, err
	}
	detail.Questions = projections
	return detail, nil
}

// projectQuestions 装载节点引用到的题目内容。
//
// 一次性按 id 批量取，而不是逐个节点查询——一份卷子可能有几十道题，
// 逐个查会变成几十条 SQL。
func (s *PaperService) projectQuestions(ctx context.Context, nodes []model.PaperNode) (map[uint]schema.PaperQuestionProjection, error) {
	out := make(map[uint]schema.PaperQuestionProjection)

	ids := domain.CollectQuestionIDs(nodes)
	if len(ids) == 0 {
		return out, nil
	}

	// 记录每个题目块声明的修订号，用于判断是否过期。
	declared := make(map[uint]int64, len(ids))
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion || nodes[i].Content.IsEmpty() {
			continue
		}
		var ref domain.QuestionRef
		if err := nodes[i].Content.Into(&ref); err == nil && ref.QuestionID != 0 {
			declared[ref.QuestionID] = ref.ContentRevision
		}
	}

	questions, err := s.repos.Questions.ListByIDs(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	for i := range questions {
		q := &questions[i]
		var options []model.Option
		if !q.Options.IsEmpty() {
			_ = q.Options.Into(&options)
		}
		var answer model.AnswerSpec
		if !q.Answer.IsEmpty() {
			_ = q.Answer.Into(&answer)
		}
		out[q.ID] = schema.PaperQuestionProjection{
			ID:                q.ID,
			QuestionType:      q.QuestionType,
			QuestionTypeLabel: q.QuestionType.Label(),
			Difficulty:        q.Difficulty,
			Stem:              q.Stem,
			Options:           options,
			Answer:            answer,
			Analysis:          q.Analysis,
			Solution:          q.Solution,
			Summary:           q.Summary,
			StemText:          q.StemText,
			ContentRevision:   q.ContentRevision,
			Stale:             declared[q.ID] != 0 && declared[q.ID] != q.ContentRevision,
		}
	}
	return out, nil
}

// UpdatePaper 修改试卷元信息。
func (s *PaperService) UpdatePaper(ctx context.Context, user *model.User, id uint, req *schema.PaperUpdateRequest) (*schema.PaperDetail, error) {
	paper, err := s.repos.Papers.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.canWrite(user, paper) {
		return nil, apperr.ErrForbidden
	}

	fields := map[string]any{"updated_by": user.ID}
	if req.Title != nil {
		fields["title"] = *req.Title
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.Status != nil {
		if !req.Status.Valid() {
			return nil, apperr.New(apperr.CodeInvalidParam, "非法的试卷状态")
		}
		fields["status"] = *req.Status
	}
	if req.NumberingEnabled != nil {
		fields["numbering_enabled"] = *req.NumberingEnabled
	}
	if req.ScoringEnabled != nil {
		fields["scoring_enabled"] = *req.ScoringEnabled
	}
	if req.QuestionDisplay != nil {
		j, _ := model.JSONOf(*req.QuestionDisplay)
		fields["question_display"] = j
	}
	switch {
	case req.ClearFolder:
		fields["folder_id"] = nil
	case req.FolderID != nil:
		folder, err := s.repos.PaperFolders.Get(ctx, *req.FolderID)
		if err != nil {
			return nil, err
		}
		if folder.SubjectID != paper.SubjectID || !sameSpace(folder, paper.Scope, paper.OwnerID) {
			return nil, apperr.New(apperr.CodeCrossSubject, "目录不在同一个空间下")
		}
		fields["folder_id"] = *req.FolderID
	}

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Papers.UpdateFields(ctx, id, fields); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   id,
			EventType: "updated",
			ActorID:   user.ID,
			Summary:   "修改了试卷信息",
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPaper(ctx, user, id)
}

// DeletePaper 软删除试卷。
func (s *PaperService) DeletePaper(ctx context.Context, user *model.User, id uint) error {
	paper, err := s.repos.Papers.Get(ctx, id)
	if err != nil {
		return err
	}
	if !s.canDelete(user, paper) {
		return apperr.ErrForbidden
	}

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Papers.SoftDelete(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   id,
			EventType: "deleted",
			ActorID:   user.ID,
			Summary:   fmt.Sprintf("删除了试卷《%s》", paper.Title),
		})
	})
}

// RestorePaper 从回收站恢复试卷。
func (s *PaperService) RestorePaper(ctx context.Context, user *model.User, id uint) error {
	paper, err := s.repos.Papers.Get(ctx, id)
	if err != nil {
		return err
	}
	if !s.canDelete(user, paper) {
		return apperr.ErrForbidden
	}
	if err := s.repos.Papers.Restore(ctx, id); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

// DuplicatePaper 复制试卷。
//
// 节点按原样复制（含同一个题目引用），不重新同步题目内容——
// 复制的语义是"复制这份排版"，不是"重新抓一遍题目"。
func (s *PaperService) DuplicatePaper(ctx context.Context, user *model.User, id uint, title string) (*schema.PaperDetail, error) {
	src, err := s.repos.Papers.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, src.SubjectID); err != nil {
		return nil, err
	}
	if src.Scope == model.PaperScopePersonal && (src.OwnerID == nil || *src.OwnerID != user.ID) && !user.IsSuperuser {
		return nil, apperr.ErrNotFound
	}
	if !user.Can(model.PermEditQuestion, src.SubjectID) {
		return nil, apperr.ErrForbidden
	}

	nodes, err := s.repos.Papers.Nodes(ctx, id)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	// 副本始终落在当前用户的个人空间，避免把别人的共享卷复制进你的个人空间时
	// 出现"共享卷的 owner 其实是某个人"这种矛盾状态。
	owner := user.ID
	if title == "" {
		title = src.Title + "（副本）"
	}

	dup := &model.Paper{
		SubjectID:        src.SubjectID,
		Scope:            model.PaperScopePersonal,
		OwnerID:          &owner,
		FolderID:         nil,
		Title:            title,
		Description:      src.Description,
		Status:           model.PaperStatusDraft,
		NumberingEnabled: src.NumberingEnabled,
		ScoringEnabled:   src.ScoringEnabled,
		QuestionDisplay:  src.QuestionDisplay,
		Revision:         1,
		CreatedBy:        user.ID,
		UpdatedBy:        user.ID,
	}

	// 节点 id 必须重新生成：它是主键，直接复制会与源卷冲突。
	newNodes := make([]model.PaperNode, 0, len(nodes))
	idMap := make(map[string]string, len(nodes))
	for i := range nodes {
		newNodes = append(newNodes, nodes[i]) // 占位，id 稍后统一重建
	}
	for i := range nodes {
		idMap[nodes[i].ID] = newUUID()
	}
	for i := range newNodes {
		old := nodes[i]
		newNodes[i].ID = idMap[old.ID]
		if old.ParentID != nil {
			if mapped, ok := idMap[*old.ParentID]; ok {
				newNodes[i].ParentID = &mapped
			}
		}
		// 软指针也要指向副本内的节点，否则会指到源卷上去。
		if old.SourceQuestionNodeID != nil {
			if mapped, ok := idMap[*old.SourceQuestionNodeID]; ok {
				newNodes[i].SourceQuestionNodeID = &mapped
			}
		}
		if old.AnchorBeforeNodeID != nil {
			if mapped, ok := idMap[*old.AnchorBeforeNodeID]; ok {
				newNodes[i].AnchorBeforeNodeID = &mapped
			}
		}
	}

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Papers.Create(ctx, dup); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if err := s.repos.Papers.ReplaceNodes(ctx, dup.ID, newNodes); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   dup.ID,
			EventType: "duplicated",
			ActorID:   user.ID,
			Summary:   fmt.Sprintf("由《%s》复制而来", src.Title),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPaper(ctx, user, dup.ID)
}
