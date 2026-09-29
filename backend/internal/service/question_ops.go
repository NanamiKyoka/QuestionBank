package service

import (
	"context"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// Update 更新题目。
//
// 关于「已发布的题目能不能改」：**能，这是刻意的**。
// 界面此前把编辑按钮锁在 status === 'draft'，而这里从来没有状态守卫，
// 两边对同一件事的说法不一致——按钮在骗人，正确的做法是让界面说实话，
// 而不是把一条明显好用的能力砍掉。改一个错别字就删除重建会丢掉题目 id，
// 连带打断所有引用它的试卷。
//
// 代价由 content_revision 承担：内容一旦变更就 +1，试卷节点据此提示
// 「题目已被改动」并支持一键同步（见 paper node sync），
// 已发布内容被悄悄改掉这件事因此始终是**可见**的。
// 为了让它可追溯，已发布题目的内容修改还会写一条审核日志（action=edit）。
//
// 只有"影响题目呈现"的字段变化才递增 content_revision：
// 试卷节点记着这个值用来判断是否需要同步分数等内容。
// 如果改来源、改可见性也递增，所有引用了这道题的试卷都会误报"已更新"。
func (s *QuestionService) Update(ctx context.Context, user *model.User, id uint, req *schema.QuestionUpdateRequest) (*schema.QuestionDetail, error) {
	current, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermEditQuestion, current.SubjectID); err != nil {
		return nil, err
	}

	// 先合并出"更新后"的完整状态，再整体校验一次。
	// 这样"只改选项没改答案"这类局部请求也能被正确拦截。
	qtype := current.QuestionType
	if req.QuestionType != nil {
		qtype = *req.QuestionType
	}

	stem := current.Stem
	if !req.Stem.IsEmpty() {
		stem = req.Stem
	}

	var currentOptions []model.Option
	if !current.Options.IsEmpty() {
		_ = current.Options.Into(&currentOptions)
	}
	var answer model.AnswerSpec
	if !current.Answer.IsEmpty() {
		_ = current.Answer.Into(&answer)
	}
	if req.Answer != nil {
		answer = *req.Answer
	}

	optionInputs := make([]schema.OptionInput, 0, len(currentOptions))
	if req.Options != nil {
		optionInputs = *req.Options
	} else {
		for i := range currentOptions {
			optionInputs = append(optionInputs, schema.OptionInput{
				Label:   currentOptions[i].Label,
				Content: currentOptions[i].Content,
			})
		}
	}

	tagIDs := req.TagIDs
	if tagIDs == nil {
		tags, err := s.repos.Questions.TagsForQuestions(ctx, []uint{id})
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		ids := make([]uint, 0, len(tags[id]))
		for _, t := range tags[id] {
			ids = append(ids, t.ID)
		}
		tagIDs = &ids
	}
	kpIDs := req.KnowledgePointIDs
	if kpIDs == nil {
		kps, err := s.repos.Questions.KnowledgePointsForQuestions(ctx, []uint{id})
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		ids := make([]uint, 0, len(kps[id]))
		for _, k := range kps[id] {
			ids = append(ids, k.ID)
		}
		kpIDs = &ids
	}

	// 结构字段（题干/选项/答案）要按"改挂之后"的母题重新校验一遍，
	// 否则会出现"挂到别的小学科的母题下"这种绕过同学科约束的写入。
	parentID := current.ParentID
	if req.ParentID != nil {
		parentID, err = resolveParentID(*req.ParentID)
		if err != nil {
			return nil, err
		}
		// 环检测必须在写库之前：parent_id 一旦落成环，递归展开子树的
		// 级联软删就会在该环上永远转下去。
		if parentID != nil {
			if err := s.checkParentLink(ctx, id, current.SubjectID, parentID); err != nil {
				return nil, err
			}
		}
	}

	prep, err := s.prepare(ctx, current.SubjectID, parentID, qtype, stem,
		optionInputs, answer, *tagIDs, *kpIDs)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{
		"updated_by":    user.ID,
		"question_type": qtype,
		"stem":          stem,
		"stem_text":     prep.stemText,
	}
	if req.ParentID != nil {
		fields["parent_id"] = parentID
	}
	if req.QuestionType != nil || req.Options != nil || req.Answer != nil || !req.Stem.IsEmpty() {
		fields["options"] = prep.optionsJSON
		fields["answer"] = prep.answerJSON
	}
	if req.Analysis != nil && !req.Analysis.IsEmpty() {
		fields["analysis"] = req.Analysis
	}
	if req.Solution != nil && !req.Solution.IsEmpty() {
		fields["solution"] = req.Solution
	}
	if req.Summary != nil && !req.Summary.IsEmpty() {
		fields["summary"] = req.Summary
	}
	if req.Difficulty != nil {
		fields["difficulty"] = *req.Difficulty
	}
	if req.Visibility != nil && req.Visibility.Valid() {
		fields["visibility"] = *req.Visibility
	}
	if req.Source != nil {
		fields["source"] = *req.Source
	}

	contentChanged := req.Stem != nil || req.Options != nil || req.Answer != nil ||
		req.QuestionType != nil || req.Analysis != nil || req.Solution != nil || req.Summary != nil

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Questions.UpdateFields(ctx, id, fields); err != nil {
			return apperr.From(err)
		}
		if req.TagIDs != nil {
			if err := s.repos.Questions.SetTags(ctx, id, *req.TagIDs); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
		}
		if req.KnowledgePointIDs != nil {
			if err := s.repos.Questions.SetKnowledgePoints(ctx, id, *req.KnowledgePointIDs); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
		}
		if contentChanged {
			return s.repos.Questions.BumpContentRevision(ctx, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 已发布题目的内容被改动，是最需要留痕的一类变更：题面已经在卷子里了。
	// 草稿/待审的修改在审核记录里本来就看得到（谁提交、谁驳回），
	// 只有"改一个错别字"这种绕过审核的路径此前完全不留痕迹。
	if contentChanged && current.Status == model.QuestionStatusPublished {
		if err := s.repos.Questions.AddReviewLog(ctx, &model.ReviewLog{
			QuestionID: id,
			ReviewerID: user.ID,
			Action:     model.ReviewActionEdit,
			FromStatus: model.QuestionStatusPublished,
			ToStatus:   model.QuestionStatusPublished,
			Comment:    req.ChangeNote,
		}); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}

	return s.Get(ctx, user, id)
}

// Delete 批量软删除。同时删除其子题，避免留下孤儿数据。
//
// 无权删除的条目会被**跳过**而不是整体报错：批量操作里有一条不满足条件
// 就全部失败，用户体验很差，而且用户往往无法判断是哪一条。
// 返回实际删除的 id，前端可以提示"已删除 N 条，跳过 M 条"。
func (s *QuestionService) Delete(ctx context.Context, user *model.User, ids []uint) (deleted []uint, skipped []uint, err error) {
	allowed := make([]uint, 0, len(ids))
	for _, id := range ids {
		q, err := s.repos.Questions.Get(ctx, id)
		if err != nil {
			if apperr.Is(err, apperr.CodeNotFound) {
				skipped = append(skipped, id)
				continue
			}
			return nil, nil, err
		}
		if !s.canDelete(user, q) {
			skipped = append(skipped, id)
			continue
		}
		allowed = append(allowed, id)
	}
	if len(allowed) == 0 {
		return nil, skipped, nil
	}

	var targets []uint
	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		var err error
		targets, err = s.repos.Questions.ChildIDs(ctx, allowed)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Questions.SoftDelete(ctx, targets)
	})
	if err != nil {
		return nil, nil, err
	}
	return allowed, skipped, nil
}

// Restore 从回收站恢复。
//
// 必须走 GetDeleted（Unscoped）：普通 Get 会被 GORM 自动追加 deleted_at IS NULL，
// 于是"恢复一条已删除的题目"永远查到记录、直接 404——路由挂着，实现却从未生效。
// GetDeleted 对"记录其实是活的"返回 ErrUnprocessable，避免"恢复一道没删的题"
// 静默成功、让调用方以为自己做了什么。
func (s *QuestionService) Restore(ctx context.Context, user *model.User, id uint) error {
	q, err := s.repos.Questions.GetDeleted(ctx, id)
	if err != nil {
		return err
	}
	if err := authorize(user, model.PermEditQuestion, q.SubjectID); err != nil {
		return err
	}
	if err := s.repos.Questions.Restore(ctx, []uint{id}); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

// Submit 提交审核：草稿 → 待审。
func (s *QuestionService) Submit(ctx context.Context, user *model.User, id uint) (*schema.QuestionDetail, error) {
	q, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.submitOnce(ctx, user, q); err != nil {
		return nil, err
	}
	return s.Get(ctx, user, id)
}

// submitOnce 单条提交审核的状态流转。单题与批量共用，
// 避免两条路径的守卫条件日后各自漂移。
func (s *QuestionService) submitOnce(ctx context.Context, user *model.User, q *model.Question) error {
	if err := authorize(user, model.PermEditQuestion, q.SubjectID); err != nil {
		return err
	}
	if q.Status != model.QuestionStatusDraft {
		return apperr.New(apperr.CodeConflict, "只有草稿状态才能提交审核").
			WithDetail("当前状态 %s", q.Status)
	}
	if err := s.repos.Questions.UpdateFields(ctx, q.ID, map[string]any{
		"status":     model.QuestionStatusPending,
		"updated_by": user.ID,
	}); err != nil {
		return apperr.From(err)
	}
	return nil
}

// Review 审核题目。
//
// 通过时累加审核次数；达到学科配置的阈值才发布，否则继续留在待审队列等下一次审核。
// 这样"重要学科要求两人复核"可以只通过配置表达，无需改代码。
//
// 驳回则把审核次数**清零**：审核次数回答的是"当前这一稿已经过了几道关"，
// 题目被打回草稿后内容还会继续改，之前那次通过并不能为改过之后的稿子背书。
// 不清零的话，阈值=2 时「通过1次 → 驳回 → 再通过1次」就会直接发布，
// 双人复核被一次驳回架空。
// 注意清零的只是计数，review_logs 里的驳回记录照常保留——
// 它是审计流水，正是"这次被谁打回过"唯一的证据来源，不能跟着计数一起抹掉。
func (s *QuestionService) Review(ctx context.Context, user *model.User, id uint, req *schema.ReviewRequest) (*schema.QuestionDetail, error) {
	q, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.reviewOnce(ctx, user, q, req.Action, req.Comment); err != nil {
		return nil, err
	}
	return s.Get(ctx, user, id)
}

// reviewOnce 单条审核的状态流转 + 审核日志，单题与批量共用。
func (s *QuestionService) reviewOnce(ctx context.Context, user *model.User, q *model.Question, action, comment string) error {
	if err := authorize(user, model.PermReviewQuestion, q.SubjectID); err != nil {
		return err
	}
	if q.Status != model.QuestionStatusDraft && q.Status != model.QuestionStatusPending {
		return apperr.New(apperr.CodeConflict, "该状态下的题目无需审核").
			WithDetail("当前状态 %s", q.Status)
	}

	from := q.Status
	to := model.QuestionStatusDraft
	// resetReview 表示本次要把审核次数清零（驳回）。计数与状态在同一次
	// UpdateFields 里落库，避免出现"状态已退回草稿、计数还是旧的"的中间态。
	resetReview := false

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		switch action {
		case model.ReviewActionApprove:
			if err := s.repos.Questions.IncrementReviewCount(ctx, q.ID); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
			subject, err := s.repos.Subjects.Get(ctx, q.SubjectID)
			if err != nil {
				return err
			}
			// q.ReviewCount 是本次审核前的值，因此 +1 后再与阈值比较。
			if q.ReviewCount+1 >= subject.ReviewThreshold {
				to = model.QuestionStatusPublished
			} else {
				to = model.QuestionStatusPending
			}
		case model.ReviewActionReject:
			to = model.QuestionStatusDraft
			resetReview = true
		default:
			return apperr.New(apperr.CodeInvalidParam, "不支持的审核动作")
		}

		fields := map[string]any{
			"status":     to,
			"updated_by": user.ID,
		}
		if resetReview {
			fields["review_count"] = 0
		}
		if err := s.repos.Questions.UpdateFields(ctx, q.ID, fields); err != nil {
			return apperr.From(err)
		}

		return s.repos.Questions.AddReviewLog(ctx, &model.ReviewLog{
			QuestionID: q.ID,
			ReviewerID: user.ID,
			Action:     action,
			FromStatus: from,
			ToStatus:   to,
			Comment:    comment,
		})
	})
}

// BatchReview 批量提交审核 / 批量通过 / 批量驳回。
//
// 语义与权限完全复用单题的 submitOnce / reviewOnce，学科阈值也照常生效：
// 一次批量通过并不会绕过"两次复核"的要求，只是把两次点击合成了一次。
//
// 逐条独立处理、逐条跳过而不是整体失败：批量选中的题天然是混的
// （混进了已发布的、混进了别的学科的），让整批因为一条失败而回滚，
// 用户既看不到结果也说不清是哪一条出了问题。
func (s *QuestionService) BatchReview(ctx context.Context, user *model.User, ids []uint, action, comment string) (handled []uint, skipped []uint, err error) {
	for _, id := range ids {
		q, getErr := s.repos.Questions.Get(ctx, id)
		if getErr != nil {
			if apperr.Is(getErr, apperr.CodeNotFound) {
				skipped = append(skipped, id)
				continue
			}
			return handled, skipped, getErr
		}
		var actionErr error
		switch action {
		case "submit":
			actionErr = s.submitOnce(ctx, user, q)
		case model.ReviewActionApprove, model.ReviewActionReject:
			actionErr = s.reviewOnce(ctx, user, q, action, comment)
		default:
			return handled, skipped, apperr.New(apperr.CodeInvalidParam, "不支持的批量审核动作")
		}
		if actionErr != nil {
			// 权限/状态类错误是"这一条不行"，不是"整个请求不行"；
			// 但仓储或事务层面的故障必须立刻冒泡，不能假装跳过。
			if apperr.Is(actionErr, apperr.CodeForbidden) ||
				apperr.Is(actionErr, apperr.CodeConflict) ||
				apperr.Is(actionErr, apperr.CodeUnprocessable) ||
				apperr.Is(actionErr, apperr.CodeInvalidParam) {
				skipped = append(skipped, id)
				continue
			}
			return handled, skipped, actionErr
		}
		handled = append(handled, id)
	}
	return handled, skipped, nil
}

// SetArchived 归档 / 取消归档。
//
// 权限用 review_question 而不是 edit_question：归档是状态机上的管控动作，
// 改错别字和下架一道已经进了试卷的题不是一回事。后者会影响所有引用它的试卷，
// 必须落在与"审核发布"同一档的权限上。
//
// 状态机刻意收得很窄：只有已发布的题能被归档。
// 待审/草稿本来就没对外，去"归档"它们只会让归档筛选里混进从未上线的题；
// 取消归档则一律回到已发布，因为归档之前它就是已发布的。
func (s *QuestionService) SetArchived(ctx context.Context, user *model.User, id uint, archived bool, comment string) (*schema.QuestionDetail, error) {
	q, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermReviewQuestion, q.SubjectID); err != nil {
		return nil, err
	}

	from, to := q.Status, model.QuestionStatusPublished
	action := model.ReviewActionRestore
	if archived {
		from, to, action = q.Status, model.QuestionStatusArchived, model.ReviewActionArchive
		if q.Status != model.QuestionStatusPublished {
			return nil, apperr.New(apperr.CodeConflict, "只有已发布的题目才能归档").
				WithDetail("当前状态 %s", q.Status)
		}
	} else if q.Status != model.QuestionStatusArchived {
		return nil, apperr.New(apperr.CodeConflict, "该题目未处于归档状态").
			WithDetail("当前状态 %s", q.Status)
	}

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Questions.UpdateFields(ctx, id, map[string]any{
			"status":     to,
			"updated_by": user.ID,
		}); err != nil {
			return apperr.From(err)
		}
		return s.repos.Questions.AddReviewLog(ctx, &model.ReviewLog{
			QuestionID: id,
			ReviewerID: user.ID,
			Action:     action,
			FromStatus: from,
			ToStatus:   to,
			Comment:    comment,
		})
	})
	if err != nil {
		return nil, err
	}

	// 归档只改状态、不改内容，因此**不能**碰 content_revision：
	// 递增会让所有引用它的试卷误报"题目已更新"，逼着老师去同步一个没变过的题面。
	return s.Get(ctx, user, id)
}

// ReviewLogs 返回题目的审核历史。
func (s *QuestionService) ReviewLogs(ctx context.Context, user *model.User, id uint, limit int) ([]schema.ReviewLogResponse, error) {
	q, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, q.SubjectID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	logs, err := s.repos.Questions.ListReviewLogs(ctx, id, limit)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.ReviewLogResponse, 0, len(logs))
	for i := range logs {
		l := &logs[i]
		item := schema.ReviewLogResponse{
			ID:         l.ID,
			Action:     l.Action,
			FromStatus: l.FromStatus,
			ToStatus:   l.ToStatus,
			Comment:    l.Comment,
			ReviewerID: l.ReviewerID,
			CreatedAt:  l.CreatedAt,
		}
		if l.Reviewer != nil {
			item.Reviewer = l.Reviewer.DisplayName()
		}
		out = append(out, item)
	}
	return out, nil
}

// BatchUpdateSource 批量修改来源。无权修改的条目跳过。
func (s *QuestionService) BatchUpdateSource(ctx context.Context, user *model.User, ids []uint, source string) (updated int, skipped []uint, err error) {
	for _, id := range ids {
		q, err := s.repos.Questions.Get(ctx, id)
		if err != nil {
			skipped = append(skipped, id)
			continue
		}
		if !user.Can(model.PermEditQuestion, q.SubjectID) {
			skipped = append(skipped, id)
			continue
		}
		// 来源不影响题目呈现，因此不递增 content_revision。
		if err := s.repos.Questions.UpdateFields(ctx, id, map[string]any{
			"source":     source,
			"updated_by": user.ID,
		}); err != nil {
			return updated, skipped, apperr.From(err)
		}
		updated++
	}
	return updated, skipped, nil
}

// Stats 学科题库统计，供仪表盘使用。
type Stats struct {
	Total    int64                          `json:"total"`
	ByStatus map[model.QuestionStatus]int64 `json:"by_status"`
	ByType   map[model.QuestionType]int64   `json:"by_type"`
}

// Stats 返回学科题目统计。
func (s *QuestionService) Stats(ctx context.Context, user *model.User, subjectID uint) (*Stats, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	byStatus, err := s.repos.Questions.StatusCounts(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	byType, err := s.repos.Questions.TypeCounts(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	var total int64
	for _, n := range byStatus {
		total += n
	}
	return &Stats{Total: total, ByStatus: byStatus, ByType: byType}, nil
}
