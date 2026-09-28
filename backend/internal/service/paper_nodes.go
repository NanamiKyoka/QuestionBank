package service

import (
	"context"
	"fmt"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// ReplaceNodes 整树替换，带乐观锁。
//
// 排版天然是"整篇重排"：插入一道题会让后面所有题号变化，用增量补丁
// 反而更难保证一致。所以采用整树覆盖 + revision 条件更新：
// 只有客户端看到的版本与服务端一致时才允许写入。
func (s *PaperService) ReplaceNodes(ctx context.Context, user *model.User, paperID uint, req *schema.ReplaceNodesRequest) (*schema.ReplaceNodesResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if !s.canWrite(user, paper) {
		return nil, apperr.ErrForbidden
	}

	// 1) 结构校验 + 派生解析条目 + 分配 position
	nodes, err := domain.NormalizeTree(req.Nodes)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateQuestionRefs(nodes); err != nil {
		return nil, err
	}

	// 2) 校验引用的题目都存在且属于同一学科
	if err := s.checkQuestionRefs(ctx, paper.SubjectID, nodes); err != nil {
		return nil, err
	}

	newRevision := req.ExpectedRevision + 1
	totalScore := sumScore(nodes, paper.ScoringEnabled)

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		// 3) 乐观锁：revision 不匹配则整体失败，不留半个改动
		ok, err := s.repos.Papers.UpdateWithRevision(ctx, paperID, req.ExpectedRevision, map[string]any{
			"updated_by":  user.ID,
			"total_score": totalScore,
		})
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if !ok {
			return apperr.ErrConflict.WithDetail("试卷已被他人修改")
		}

		if err := s.repos.Papers.ReplaceNodes(ctx, paperID, nodes); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}

		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   paperID,
			EventType: "nodes_replaced",
			ActorID:   user.ID,
			Summary:   fmt.Sprintf("调整了排版（%d 个节点）", len(nodes)),
			Payload:   mustJSON(map[string]any{"node_count": len(nodes), "batch_id": req.BatchID}),
		})
	})
	if err != nil {
		return nil, err
	}

	saved, err := s.repos.Papers.Nodes(ctx, paperID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	out := make([]schema.PaperNodeResponse, 0, len(saved))
	for i := range saved {
		out = append(out, schema.NewPaperNodeResponse(&saved[i]))
	}
	return &schema.ReplaceNodesResponse{Revision: newRevision, Nodes: out}, nil
}

// checkQuestionRefs 校验题目引用的合法性与跨学科引用。
func (s *PaperService) checkQuestionRefs(ctx context.Context, subjectID uint, nodes []model.PaperNode) error {
	ids := domain.CollectQuestionIDs(nodes)
	if len(ids) == 0 {
		return nil
	}
	missing, err := s.repos.Questions.ExistsInSubject(ctx, ids, subjectID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if len(missing) > 0 {
		return apperr.New(apperr.CodeMissingRef, "试卷引用了不存在或不属于本学科的题目").
			WithDetail("question_ids=%v", missing)
	}
	return nil
}

// sumScore 汇总试卷总分。
//
// 分值存在题目节点的 props.score 上（同一道题在不同卷子里可以给不同分值，
// 所以不能存在题库里）。未开启分值功能时直接返回 0。
func sumScore(nodes []model.PaperNode, scoringEnabled bool) float64 {
	if !scoringEnabled {
		return 0
	}
	var total float64
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion || nodes[i].Props.IsEmpty() {
			continue
		}
		var props struct {
			Score float64 `json:"score"`
		}
		if err := nodes[i].Props.Into(&props); err == nil {
			total += props.Score
		}
	}
	return total
}

// CheckQuestionRevisions 检查试卷里的题目是否有更新或被删除。
func (s *PaperService) CheckQuestionRevisions(ctx context.Context, user *model.User, paperID uint) (*schema.QuestionRevisionsResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}

	nodes, err := s.repos.Papers.Nodes(ctx, paperID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	declared := make(map[uint]int64)
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion || nodes[i].Content.IsEmpty() {
			continue
		}
		var ref domain.QuestionRef
		if err := nodes[i].Content.Into(&ref); err == nil && ref.QuestionID != 0 {
			declared[ref.QuestionID] = ref.ContentRevision
		}
	}

	ids := make([]uint, 0, len(declared))
	for id := range declared {
		ids = append(ids, id)
	}

	current, err := s.repos.Questions.ExistingIDs(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	resp := &schema.QuestionRevisionsResponse{Stale: []uint{}, Missing: []uint{}}
	for _, id := range ids {
		rev, ok := current[id]
		if !ok {
			resp.Missing = append(resp.Missing, id)
			continue
		}
		// declared 为 0 说明节点没记录修订号（历史数据），不视为过期。
		if declared[id] != 0 && declared[id] != rev {
			resp.Stale = append(resp.Stale, id)
		}
	}
	return resp, nil
}

// SyncQuestionNodes 把节点上记录的题目修订号刷新为当前值。
//
// 只改修订号，不改题目内容本身——题目内容始终从题库实时读取。
// 这一步的作用是让"题目已更新"的提示消失，表示用户已确认。
func (s *PaperService) SyncQuestionNodes(ctx context.Context, user *model.User, paperID uint, questionIDs []uint) (*schema.SyncQuestionNodesResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if !s.canWrite(user, paper) {
		return nil, apperr.ErrForbidden
	}

	nodes, err := s.repos.Papers.Nodes(ctx, paperID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	target := make(map[uint]bool, len(questionIDs))
	for _, id := range questionIDs {
		target[id] = true
	}

	// 取出需要刷新的题目的当前修订号。
	var want []uint
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion || nodes[i].Content.IsEmpty() {
			continue
		}
		var ref domain.QuestionRef
		if err := nodes[i].Content.Into(&ref); err != nil || ref.QuestionID == 0 {
			continue
		}
		if len(target) == 0 || target[ref.QuestionID] {
			want = append(want, ref.QuestionID)
		}
	}
	if len(want) == 0 {
		return &schema.SyncQuestionNodesResponse{Revision: paper.Revision, Synced: []uint{}}, nil
	}

	current, err := s.repos.Questions.ExistingIDs(ctx, want)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	synced := make([]uint, 0, len(want))
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion || nodes[i].Content.IsEmpty() {
			continue
		}
		var ref domain.QuestionRef
		if err := nodes[i].Content.Into(&ref); err != nil || ref.QuestionID == 0 {
			continue
		}
		rev, ok := current[ref.QuestionID]
		if !ok || rev == ref.ContentRevision {
			continue
		}
		ref.ContentRevision = rev
		j, err := model.JSONOf(ref)
		if err != nil {
			continue
		}
		nodes[i].Content = j
		synced = append(synced, ref.QuestionID)
	}

	newRevision := paper.Revision + 1
	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		ok, err := s.repos.Papers.UpdateWithRevision(ctx, paperID, paper.Revision, map[string]any{
			"updated_by": user.ID,
		})
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if !ok {
			return apperr.ErrConflict.WithDetail("试卷已被他人修改")
		}
		if err := s.repos.Papers.ReplaceNodes(ctx, paperID, nodes); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   paperID,
			EventType: "questions_synced",
			ActorID:   user.ID,
			Summary:   fmt.Sprintf("同步了 %d 道题目的最新内容", len(synced)),
		})
	})
	if err != nil {
		return nil, err
	}

	return &schema.SyncQuestionNodesResponse{Revision: newRevision, Synced: synced}, nil
}

// FinalizeVersion 定稿：把当前节点树冻结为一个不可变版本。
func (s *PaperService) FinalizeVersion(ctx context.Context, user *model.User, paperID uint, req *schema.FinalizeRequest) (*schema.VersionResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if !s.canWrite(user, paper) {
		return nil, apperr.ErrForbidden
	}

	nodes, err := s.repos.Papers.Nodes(ctx, paperID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if len(domain.CollectQuestionIDs(nodes)) == 0 {
		return nil, apperr.New(apperr.CodeUnprocessable, "空试卷无法定稿")
	}

	projections, err := s.projectQuestions(ctx, nodes)
	if err != nil {
		return nil, err
	}

	// 快照是**自包含**的：把题目内容一并冻结进去。
	// 这样即使题库里的题目之后被改动或删除，历史版本依然能完整还原与导出。
	snapshot, err := model.JSONOf(map[string]any{
		"title":             paper.Title,
		"nodes":             nodes,
		"questions":         projections,
		"numbering_enabled": paper.NumberingEnabled,
		"scoring_enabled":   paper.ScoringEnabled,
		"question_display":  paper.QuestionDisplay,
		"total_score":       paper.TotalScore,
		"source_revision":   paper.Revision,
		"schema_version":    1,
	})
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	var created *model.PaperVersion
	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		versionNo, err := s.repos.Papers.NextVersionNo(ctx, paperID)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		v := &model.PaperVersion{
			PaperID:       paperID,
			VersionNo:     versionNo,
			Title:         paper.Title,
			Note:          req.Note,
			Snapshot:      snapshot,
			SchemaVersion: 1,
			CreatedBy:     user.ID,
		}
		if err := s.repos.Papers.CreateVersion(ctx, v); err != nil {
			// 并发定稿撞上同一个版本号，让客户端重试即可。
			return apperr.ErrConflict.WithCause(err)
		}
		created = v

		if err := s.repos.Papers.UpdateFields(ctx, paperID, map[string]any{
			"finalized_version": v.VersionNo,
			"status":            model.PaperStatusFinalized,
			"updated_by":        user.ID,
		}); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}

		return s.repos.Papers.AddEvent(ctx, &model.PaperEvent{
			PaperID:   paperID,
			EventType: "finalized",
			ActorID:   user.ID,
			Summary:   fmt.Sprintf("定稿为版本 v%d", v.VersionNo),
		})
	})
	if err != nil {
		return nil, err
	}

	resp := newVersionResponse(created)
	resp.QuestionCount = len(domain.CollectQuestionIDs(nodes))
	return &resp, nil
}

// ListVersions 版本列表。
func (s *PaperService) ListVersions(ctx context.Context, user *model.User, paperID uint) ([]schema.VersionResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}

	list, err := s.repos.Papers.ListVersions(ctx, paperID, 100)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	out := make([]schema.VersionResponse, 0, len(list))
	for i := range list {
		out = append(out, newVersionResponse(&list[i]))
	}
	return out, nil
}

// GetVersion 版本详情（含冻结快照）。
func (s *PaperService) GetVersion(ctx context.Context, user *model.User, paperID uint, versionNo int) (*schema.VersionDetailResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}

	v, err := s.repos.Papers.GetVersion(ctx, paperID, versionNo)
	if err != nil {
		return nil, err
	}
	return &schema.VersionDetailResponse{
		VersionResponse: newVersionResponse(v),
		Snapshot:        v.Snapshot,
	}, nil
}

// ListEvents 操作时间线（键集分页）。
func (s *PaperService) ListEvents(ctx context.Context, user *model.User, paperID uint, limit int, beforeID uint) ([]schema.EventResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	list, err := s.repos.Papers.ListEvents(ctx, paperID, limit, beforeID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.EventResponse, 0, len(list))
	for i := range list {
		e := &list[i]
		item := schema.EventResponse{
			ID:        e.ID,
			EventType: e.EventType,
			Summary:   e.Summary,
			ActorID:   e.ActorID,
			Payload:   e.Payload,
			CreatedAt: e.CreatedAt,
		}
		if e.Actor != nil {
			item.Actor = e.Actor.DisplayName()
		}
		out = append(out, item)
	}
	return out, nil
}

func newVersionResponse(v *model.PaperVersion) schema.VersionResponse {
	return schema.VersionResponse{
		ID:            v.ID,
		PaperID:       v.PaperID,
		VersionNo:     v.VersionNo,
		Title:         v.Title,
		Note:          v.Note,
		SchemaVersion: v.SchemaVersion,
		CreatedBy:     v.CreatedBy,
		CreatedAt:     v.CreatedAt,
	}
}

// mustJSON 编码失败时返回 null，用于"尽力而为"的事件负载。
func mustJSON(v any) model.JSON {
	j, err := model.JSONOf(v)
	if err != nil {
		return nil
	}
	return j
}
