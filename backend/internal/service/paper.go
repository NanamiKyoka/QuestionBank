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
	// IncludeDeleted 是否包含已软删除的试卷（回收站）。
	IncludeDeleted bool
	// OnlyDeleted 只返回已软删除的试卷（回收站）。
	OnlyDeleted bool
}

// ListPapers 分页查询试卷。
func (s *PaperService) ListPapers(ctx context.Context, user *model.User, subjectID uint, q ListPapersQuery, limit, offset int) ([]schema.PaperBrief, int64, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, 0, err
	}

	filter := repository.PaperFilter{
		SubjectID:      subjectID,
		Scope:          q.Scope,
		FolderID:       q.FolderID,
		Unfiled:        q.Unfiled,
		Keyword:        q.Keyword,
		Status:         q.Status,
		IncludeDeleted: q.IncludeDeleted,
		OnlyDeleted:    q.OnlyDeleted,
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

// authorizePaperRead 校验当前用户是否可以读取这份试卷。
//
// 分两层，缺一不可：
//  1. 学科级读权限——决定"这个人是否属于这个学科"；
//  2. 空间归属——个人空间的卷子只有本人（以及超管）能读。
//
// 第 2 层曾经只写在 GetPaper 里，而版本列表、版本详情、操作时间线、
// 版本导出都只做了第 1 层。后果是同学科的任何成员只要猜到试卷 id，
// 就能读到别人的个人卷快照、完整操作流水，甚至把定稿版本导出走。
// 个人空间的意义就在于"只有我看得见"，少这一层等于这个空间不存在，
// 所以这里抽成公共函数，让所有读路径都必须经过它。
func authorizePaperRead(user *model.User, paper *model.Paper) error {
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return err
	}
	if paper.Scope == model.PaperScopePersonal &&
		(paper.OwnerID == nil || *paper.OwnerID != user.ID) && !user.IsSuperuser {
		// 返回 404 而不是 403：403 等于确认"这张卷子存在"，
		// 反而给枚举提供了信号。
		return apperr.ErrNotFound
	}
	return nil
}

// GetPaper 试卷详情：元信息 + 节点树 + 题目内容投影。
func (s *PaperService) GetPaper(ctx context.Context, user *model.User, id uint) (*schema.PaperDetail, error) {
	paper, err := s.repos.Papers.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorizePaperRead(user, paper); err != nil {
		return nil, err
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

	projections, err := s.projectQuestions(ctx, nodes, user)
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
//
// 内容取自**节点上的冻结副本**，而不是题目表当前的值：试卷是稿件，
// 题库之后怎么改都不应该回头改动已经排好的卷子（导出与阅读都按冻结内容走）。
// 题目表仍然要查，但只用来看两件事——它是否还存在、修订号有没有变
// （用来标"可同步最新内容"）以及可见性。这三点都不需要完整内容。
//
// viewer 用于可见性过滤：写入口（checkQuestionVisibility）已经拦住了"把私有题
// 塞进他人可读的卷子"，但那只管**新增**。存量数据里已经有两种绕过路径：
// 题是公开时被排进了共享卷、事后才改成私有；或者这份卷子是在修复之前建的。
// 所以读侧必须再挡一次——注意判定要看**当前**的题目行，
// 因为"题变私有"这件事正是靠它才能被发现。
func (s *PaperService) projectQuestions(ctx context.Context, nodes []model.PaperNode, viewer *model.User) (map[uint]schema.PaperQuestionProjection, error) {
	out := make(map[uint]schema.PaperQuestionProjection)

	ids := domain.CollectQuestionIDs(nodes)
	if len(ids) == 0 {
		return out, nil
	}

	// 记录每个题目块声明的修订号，用于判断是否过期。
	declared := make(map[uint]int64, len(ids))
	frozen := make(map[uint]frozenQuestion, len(ids))
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion || nodes[i].Content.IsEmpty() {
			continue
		}
		var ref domain.QuestionRef
		if err := nodes[i].Content.Into(&ref); err != nil || ref.QuestionID == 0 {
			continue
		}
		declared[ref.QuestionID] = ref.ContentRevision
		if !nodes[i].QuestionContent.IsEmpty() {
			var fq frozenQuestion
			if err := nodes[i].QuestionContent.Into(&fq); err == nil {
				frozen[ref.QuestionID] = fq
			}
		}
	}

	list, err := s.repos.Questions.ListByIDs(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	live := make(map[uint]*model.Question, len(list))
	for i := range list {
		live[list[i].ID] = &list[i]
	}

	for _, id := range ids {
		cur, alive := live[id]
		if alive && !questionReadableBy(cur, viewer) {
			continue
		}

		if fq, ok := frozen[id]; ok {
			out[id] = projectionFromFrozen(fq, declared, cur)
			continue
		}
		// 没有冻结副本：历史数据，或题目已被删除。
		if !alive {
			continue // 题目没了且没有快照，导出时会显示"引用失效"
		}
		out[id] = projectionFromFrozen(freezePayload(cur), declared, cur)
	}
	return out, nil
}

// projectionFromFrozen 把冻结内容转成前端投影。
//
// Stale 判定用「节点声明的修订号」与「题目当前修订号」比较：
// 冻结之后题库又改过，界面就该提示可以同步最新内容。
// 题目已被删除时一律视为过期——内容已经不完整了。
func projectionFromFrozen(fq frozenQuestion, declared map[uint]int64, live *model.Question) schema.PaperQuestionProjection {
	var options []model.Option
	if !fq.Options.IsEmpty() {
		_ = fq.Options.Into(&options)
	}
	var answer model.AnswerSpec
	if !fq.Answer.IsEmpty() {
		_ = fq.Answer.Into(&answer)
	}
	return schema.PaperQuestionProjection{
		ID:                fq.ID,
		QuestionType:      fq.QuestionType,
		QuestionTypeLabel: fq.QuestionTypeLabel,
		Difficulty:        fq.Difficulty,
		Stem:              fq.Stem,
		Options:           options,
		Answer:            answer,
		Analysis:          fq.Analysis,
		Solution:          fq.Solution,
		Summary:           fq.Summary,
		StemText:          fq.StemText,
		ContentRevision:   fq.ContentRevision,
		Stale:             live == nil || (declared[fq.ID] != 0 && declared[fq.ID] != live.ContentRevision),
		Visibility:        fq.Visibility,
		CreatedBy:         fq.CreatedBy,
	}
}

// questionReadableBy 判断 viewer 是否有权读到这道题的完整内容。
//
// 与 repository 层题目列表的过滤条件保持一致：公开题人人可读，
// 私有题仅创建者与拥有 view_private_any 的人可读。超管同样受此约束——
// 超管可以去看题库，但试卷投影是把内容**摊给卷子的读者**，
// 因此判定标准是"这个读者有没有资格看"，而不是"有没有权限看全库"。
func questionReadableBy(q *model.Question, viewer *model.User) bool {
	if q.Visibility != model.VisibilityPrivate {
		return true
	}
	if viewer == nil {
		return false
	}
	return q.CreatedBy == viewer.ID || viewer.IsSuperuser ||
		viewer.Can(model.PermViewPrivateAny, q.SubjectID)
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
	// 必须用 GetDeleted：已软删除的记录用普通 Get 查不到，
	// 那会让这条恢复接口永远返回 404。
	paper, err := s.repos.Papers.GetDeleted(ctx, id)
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
