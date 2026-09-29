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

	// 2) 校验引用的题目都存在、属于同一学科，且对这份试卷的读者可见
	if err := s.checkQuestionRefs(ctx, user, paper, nodes); err != nil {
		return nil, err
	}

	// 3) 冻结题目内容。放在校验之后：先确认这道题确实可用，
	//    再把它抄进卷子，否则会把不该留存的题目内容先落到内存里。
	if err := s.freezeQuestionContent(ctx, paperID, nodes); err != nil {
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

// freezeQuestionContent 为题目块冻结题目内容。
//
// 何时重新冻结——这是本函数唯一需要讲清楚的规则：
//
//   - 节点是**新增**的（这张卷子里还没有这个节点 id）：冻结。
//     刚拖进来的题，理应记录它此刻的样子。
//   - 节点引用的**题目换了**（question_id 变了）：冻结。
//     内容跟着新题目走，保留旧快照等于张冠李戴。
//   - 其余情况（同一道题、同一节点再次保存）：**不重新冻结**。
//     自动保存是整棵树覆盖式的，用户每敲一下键盘都会提交一次；
//     若每次都重新冻结，题库里的改动就会被自动保存悄悄带进卷子，
//     "卷子不受题库影响"这条语义当场作废。题库变了要靠
//     SyncQuestionNodes 显式同步，那里会明确告诉用户"要更新几道题"。
//
// 冻结不了的情况（题目已被软删除）不是错误：节点保留，
// QuestionContent 留空，读的时候回落到实时查询并显示"引用失效"。
func (s *PaperService) freezeQuestionContent(ctx context.Context, paperID uint, nodes []model.PaperNode) error {
	ids := domain.CollectQuestionIDs(nodes)
	if len(ids) == 0 {
		return nil
	}

	// 先看这张卷子已经冻结过什么：节点 id -> 旧快照。
	previous, err := s.repos.Papers.Nodes(ctx, paperID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	prevByNodeID := make(map[string]model.PaperNode, len(previous))
	for i := range previous {
		prevByNodeID[previous[i].ID] = previous[i]
	}

	// 只给"需要重新冻结"的节点去查题目，避免每次自动保存都把
	// 整张卷子引用的题目重新读一遍。
	needFreeze := make(map[uint]struct{}, len(ids))
	for i := range nodes {
		n := &nodes[i]
		if n.NodeType != model.NodeTypeQuestion {
			continue
		}
		var ref domain.QuestionRef
		if n.Content.IsEmpty() {
			continue
		}
		if err := n.Content.Into(&ref); err != nil || ref.QuestionID == 0 {
			continue
		}
		prev, existed := prevByNodeID[n.ID]
		// 引用换了题，或者这个节点还没有快照（例如历史数据），都需要重新冻结。
		if !existed || prev.QuestionID == nil || *prev.QuestionID != ref.QuestionID || prev.QuestionContent.IsEmpty() {
			n.QuestionID = &ref.QuestionID
			needFreeze[ref.QuestionID] = struct{}{}
		} else {
			// 沿用旧快照，连同它冻结时的修订号一起带走。
			n.QuestionID = prev.QuestionID
			n.QuestionContent = prev.QuestionContent
		}
	}
	if len(needFreeze) == 0 {
		return nil
	}

	list, err := s.repos.Questions.ListByIDs(ctx, mapKeys(needFreeze))
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	byID := make(map[uint]model.Question, len(list))
	for i := range list {
		byID[list[i].ID] = list[i]
	}

	for i := range nodes {
		n := &nodes[i]
		if n.QuestionID == nil || !n.QuestionContent.IsEmpty() {
			continue
		}
		q, ok := byID[*n.QuestionID]
		if !ok {
			continue // 已被删除：留空，读时显示"引用失效"
		}
		snap, err := model.JSONOf(freezePayload(&q))
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		n.QuestionContent = snap
	}
	return nil
}

// frozenQuestion 冻结到节点上的题目内容。
//
// 字段与 schema.PaperQuestionProjection 对齐：投影层读出来就能直接渲染，
// 不必再认识题目表。Deleted 单独记一份，因为"曾经存在但现在没了"
// 与"从未存在"在导出时的处理完全不同。
type frozenQuestion struct {
	ID                uint               `json:"id"`
	QuestionType      model.QuestionType `json:"question_type"`
	QuestionTypeLabel string             `json:"question_type_label"`
	Difficulty        int                `json:"difficulty"`
	Stem              model.JSON         `json:"stem"`
	Options           model.JSON         `json:"options"`
	Answer            model.JSON         `json:"answer"`
	Analysis          model.JSON         `json:"analysis"`
	Solution          model.JSON         `json:"solution"`
	Summary           model.JSON         `json:"summary"`
	StemText          string             `json:"stem_text"`
	ContentRevision   int64              `json:"content_revision"`
	Visibility        model.Visibility   `json:"visibility"`
	CreatedBy         uint               `json:"created_by"`
}

func freezePayload(q *model.Question) frozenQuestion {
	return frozenQuestion{
		ID:                q.ID,
		QuestionType:      q.QuestionType,
		QuestionTypeLabel: q.QuestionType.Label(),
		Difficulty:        q.Difficulty,
		Stem:              q.Stem,
		Options:           q.Options,
		Answer:            q.Answer,
		Analysis:          q.Analysis,
		Solution:          q.Solution,
		Summary:           q.Summary,
		StemText:          q.StemText,
		ContentRevision:   q.ContentRevision,
		Visibility:        q.Visibility,
		CreatedBy:         q.CreatedBy,
	}
}

func mapKeys(m map[uint]struct{}) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// checkQuestionRefs 校验题目引用的合法性与跨学科引用。
func (s *PaperService) checkQuestionRefs(ctx context.Context, user *model.User, paper *model.Paper, nodes []model.PaperNode) error {
	ids := domain.CollectQuestionIDs(nodes)
	if len(ids) == 0 {
		return nil
	}
	missing, err := s.repos.Questions.ExistsInSubject(ctx, ids, paper.SubjectID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if len(missing) > 0 {
		return apperr.New(apperr.CodeMissingRef, "试卷引用了不存在或不属于本学科的题目").
			WithDetail("question_ids=%v", missing)
	}
	return s.checkQuestionVisibility(ctx, user, paper, ids)
}

// checkQuestionVisibility 拦截私有题被放进他人也能读到的试卷。
//
// 读取路径（QuestionService.Get）对私有题是"仅创建者 / view_private_any 可见"，
// 但试卷投影（projectQuestions → ListByIDs）**不重复这道过滤**：只要题目进了节点树，
// 该学科的每个成员都能在试卷里读到它的完整题干与答案。把私有题塞进共享试卷，
// 等于绕开了可见性这道闸门——所以这里按"这份卷子还有谁会读"来判定，而不是
// 只看操作者自己能不能看见：
//
//   - 共享试卷：读者是全体学科成员，私有题一律拒绝。
//     即使操作者是超管也不放行——他有权"看"，不等于有权替别人"公开"；
//     真要共享这道题，先把可见性改成 public，这一步必须是显式的。
//   - 个人试卷：只有本人会读，因此允许放自己的私有题，
//     但仍然拒绝引用别人的私有题（哪怕操作者有 view_private_any——
//     那条权限是给查看用的，不该顺手把别人的私有题复制进自己的卷子）。
func (s *PaperService) checkQuestionVisibility(ctx context.Context, user *model.User, paper *model.Paper, ids []uint) error {
	questions, err := s.repos.Questions.ListByIDs(ctx, ids)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	byID := make(map[uint]*model.Question, len(questions))
	for i := range questions {
		byID[questions[i].ID] = &questions[i]
	}

	canViewAll := user.Can(model.PermViewPrivateAny, paper.SubjectID)
	var denied []uint
	for _, id := range ids {
		q := byID[id]
		if q == nil || q.Visibility != model.VisibilityPrivate {
			continue
		}
		if paper.Scope == model.PaperScopePersonal && (q.CreatedBy == user.ID || canViewAll) {
			continue
		}
		denied = append(denied, id)
	}
	if len(denied) == 0 {
		return nil
	}
	if paper.Scope == model.PaperScopeShared {
		return apperr.New(apperr.CodeForbidden, "私有题目不能加入共享试卷").
			WithDetail("共享试卷的读者是全体学科成员；如需共享请先把题目可见性改为公开。question_ids=%v", denied)
	}
	return apperr.New(apperr.CodeForbidden, "试卷引用了别人的私有题目").
		WithDetail("私有题目仅创建者可用。question_ids=%v", denied)
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
	if err := authorizePaperRead(user, paper); err != nil {
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

// SyncQuestionNodes 把节点上的题目内容刷新为题库的当前值。
//
// 这是"题库改了 → 卷子里也改"的唯一显式入口：用户点了同步，
// 我们才把题目的最新内容重新冻结进节点，并把修订号对齐。
//
// 之所以需要这一步而不是自动跟随：试卷是稿件，题库里的日常改动
// 不该悄悄改动已排好的卷子；但用户确认之后，就该一步到位地跟上，
// 而不是只消掉"题目已更新"的提示、卷面还是旧内容。
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

	// 取回要同步的题目的当前内容，重新冻结。
	fresh, err := s.repos.Questions.ListByIDs(ctx, want)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	byID := make(map[uint]*model.Question, len(fresh))
	for i := range fresh {
		byID[fresh[i].ID] = &fresh[i]
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
		if !ok {
			// 题目已被删除：保留旧冻结内容不动，让它标着过期，
			// 而不是把节点清空——清空等于把卷面挖一个洞。
			continue
		}
		q := byID[ref.QuestionID]
		if q != nil {
			snap, err := model.JSONOf(freezePayload(q))
			if err != nil {
				return nil, apperr.ErrInternal.WithCause(err)
			}
			nodes[i].QuestionContent = snap
			nodes[i].QuestionID = &ref.QuestionID
		}
		if rev == ref.ContentRevision {
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

	projections, err := s.projectQuestions(ctx, nodes, user)
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
	// 必须走 authorizePaperRead：个人空间的卷子只有本人能读，
	// 只做学科级校验等于任何成员都能翻出别人的版本列表。
	if err := authorizePaperRead(user, paper); err != nil {
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
//
// 快照里是**完整组卷内容**，因此这里的归属校验与列表同样不能省。
func (s *PaperService) GetVersion(ctx context.Context, user *model.User, paperID uint, versionNo int) (*schema.VersionDetailResponse, error) {
	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizePaperRead(user, paper); err != nil {
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
	// 操作流水同样属于卷子内容：能看到时间线就等于看到了这张卷子的全部改动。
	if err := authorizePaperRead(user, paper); err != nil {
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
