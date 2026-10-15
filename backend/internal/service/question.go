package service

import (
	"context"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// QuestionService 题库用例。
type QuestionService struct {
	repos *Repos
}

func NewQuestionService(repos *Repos) *QuestionService { return &QuestionService{repos: repos} }

// ListQuery 列表查询参数（HTTP 层绑定后传入）。
type ListQuery struct {
	Keyword       string
	Type          model.QuestionType
	Status        model.QuestionStatus
	Difficulty    int
	DifficultyMin int
	DifficultyMax int

	// KnowledgePointIDs 支持多选（?knowledge_point_ids=1&knowledge_point_ids=2）。
	// 仓储层本来就是按集合过滤的，多选只是把 HTTP 层的单值绑定补齐。
	KnowledgePointIDs []uint
	IncludeSub        bool
	TagIDs            []uint
	TagMatch          string

	CreatedBy uint
	// Source 来源模糊匹配。
	Source string
	// ReviewCount 精确匹配审核次数；nil 表示不过滤。
	ReviewCount *int
	// ReviewerID 被此人审核过。
	ReviewerID     uint
	RootOnly       bool
	IncludeDeleted bool
	// OnlyDeleted 只返回已软删除的题目（回收站）。与 IncludeDeleted 互斥。
	OnlyDeleted bool

	Sort model.QuestionSort
}

// List 分页查询题目。
func (s *QuestionService) List(ctx context.Context, user *model.User, subjectID uint, q ListQuery, limit, offset int) ([]schema.QuestionBrief, int64, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, 0, err
	}

	filter := model.QuestionFilter{
		SubjectID:         subjectID,
		Keyword:           strings.TrimSpace(q.Keyword),
		Type:              q.Type,
		Status:            q.Status,
		Difficulty:        q.Difficulty,
		DifficultyMin:     q.DifficultyMin,
		DifficultyMax:     q.DifficultyMax,
		TagIDs:            q.TagIDs,
		TagMatch:          q.TagMatch,
		CreatedBy:         q.CreatedBy,
		Source:            strings.TrimSpace(q.Source),
		ReviewCount:       q.ReviewCount,
		ReviewerID:        q.ReviewerID,
		RootOnly:          q.RootOnly,
		IncludeDeleted:    q.IncludeDeleted,
		OnlyDeleted:       q.OnlyDeleted,
		ViewerID:          user.ID,
		CanViewPrivateAll: user.Can(model.PermViewPrivateAny, subjectID),
	}

	if len(q.KnowledgePointIDs) > 0 {
		ids, err := s.resolveKnowledgePoints(ctx, q.KnowledgePointIDs, q.IncludeSub)
		if err != nil {
			return nil, 0, err
		}
		filter.KnowledgePointIDs = ids
	}

	list, total, err := s.repos.Questions.List(ctx, filter, q.Sort, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}
	if len(list) == 0 {
		return []schema.QuestionBrief{}, total, nil
	}

	briefs, err := s.toBriefs(ctx, list)
	if err != nil {
		return nil, 0, err
	}
	return briefs, total, nil
}

// resolveKnowledgePoints 把"知识点（可多选）+ 是否含子树"解析为 id 集合。
//
// 多选时各子树取并集：筛选语义是"命中其中任意一个知识点"，
// 因此两个父知识点各自的子树都要被展开，不能只展开其中一个。
func (s *QuestionService) resolveKnowledgePoints(ctx context.Context, kpIDs []uint, includeSub bool) ([]uint, error) {
	seen := make(map[uint]bool, len(kpIDs))
	for _, kpID := range kpIDs {
		if kpID == 0 {
			continue
		}
		kp, err := s.repos.KnowledgePoints.Get(ctx, kpID)
		if err != nil {
			return nil, err
		}
		ids := []uint{kp.ID}
		if includeSub {
			subtree, err := s.repos.KnowledgePoints.Subtree(ctx, kp.SubjectID, kp.Path)
			if err != nil {
				return nil, apperr.ErrInternal.WithCause(err)
			}
			for i := range subtree {
				ids = append(ids, subtree[i].ID)
			}
		}
		for _, id := range ids {
			seen[id] = true
		}
	}
	out := make([]uint, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}

// toBriefs 把题目列表转换为响应对象，关联数据批量装载。
//
// 整个函数只有固定条数的 SQL（标签、知识点、子题数各一条），
// 与题目数量无关——这是列表性能的关键。
func (s *QuestionService) toBriefs(ctx context.Context, list []model.Question) ([]schema.QuestionBrief, error) {
	ids := make([]uint, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}

	tags, err := s.repos.Questions.TagsForQuestions(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	kps, err := s.repos.Questions.KnowledgePointsForQuestions(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	children, err := s.repos.Questions.ChildCounts(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.QuestionBrief, 0, len(list))
	for i := range list {
		q := &list[i]
		out = append(out, schema.NewQuestionBrief(q, tags[q.ID], kps[q.ID], children[q.ID]))
	}
	return out, nil
}

// Get 读取题目详情。
func (s *QuestionService) Get(ctx context.Context, user *model.User, id uint) (*schema.QuestionDetail, error) {
	q, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, q.SubjectID); err != nil {
		return nil, err
	}
	// 私有题对非创建者不可见。返回 404 而非 403：
	// 403 等于告诉对方"这道题存在"，可以被用来枚举他人私有题库。
	if q.Visibility == model.VisibilityPrivate &&
		q.CreatedBy != user.ID &&
		!user.Can(model.PermViewPrivateAny, q.SubjectID) {
		return nil, apperr.ErrNotFound
	}

	return s.buildDetail(ctx, user, q)
}

// BatchGet 按请求顺序读取可见的题目详情。
func (s *QuestionService) BatchGet(ctx context.Context, user *model.User, ids []uint) ([]schema.QuestionDetail, error) {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]schema.QuestionDetail, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		detail, err := s.Get(ctx, user, id)
		if err != nil {
			if apperr.Is(err, apperr.CodeNotFound) || apperr.Is(err, apperr.CodeForbidden) {
				continue
			}
			return nil, err
		}
		out = append(out, *detail)
	}
	return out, nil
}

func (s *QuestionService) buildDetail(ctx context.Context, user *model.User, q *model.Question) (*schema.QuestionDetail, error) {
	tags, err := s.repos.Questions.TagsForQuestions(ctx, []uint{q.ID})
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	kps, err := s.repos.Questions.KnowledgePointsForQuestions(ctx, []uint{q.ID})
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	children, err := s.repos.Questions.Children(ctx, q.ID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	// 非选择题没有选项，options 必须初始化成空切片而不是留 nil：
	// nil 会序列化成 "options": null，前端读 .length 时直接抛异常，
	// 表现是这道题打不开详情、也进不了编辑。
	options := []model.Option{}
	if !q.Options.IsEmpty() {
		if err := q.Options.Into(&options); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}
	var answer model.AnswerSpec
	if !q.Answer.IsEmpty() {
		if err := q.Answer.Into(&answer); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}

	childBriefs := make([]schema.QuestionBrief, 0, len(children))
	if len(children) > 0 {
		childIDs := make([]uint, 0, len(children))
		for i := range children {
			childIDs = append(childIDs, children[i].ID)
		}
		ct, _ := s.repos.Questions.TagsForQuestions(ctx, childIDs)
		ck, _ := s.repos.Questions.KnowledgePointsForQuestions(ctx, childIDs)
		for i := range children {
			childBriefs = append(childBriefs, schema.NewQuestionBrief(&children[i], ct[children[i].ID], ck[children[i].ID], 0))
		}
	}

	brief := schema.NewQuestionBrief(q, tags[q.ID], kps[q.ID], len(children))
	// 详情自带 Stem，brief 里的那份是给列表用的，重复一份只是白白撑大响应体。
	brief.StemDoc = nil

	return &schema.QuestionDetail{
		QuestionBrief:   brief,
		Stem:            q.Stem,
		Options:         options,
		Answer:          answer,
		Analysis:        q.Analysis,
		Solution:        q.Solution,
		Summary:         q.Summary,
		ContentVersion:  q.ContentVersion,
		ContentRevision: q.ContentRevision,
		ReviewCount:     q.ReviewCount,
		Children:        childBriefs,
		Permissions: schema.QuestionPermissions{
			CanEdit:   user.Can(model.PermEditQuestion, q.SubjectID),
			CanDelete: s.canDelete(user, q),
			CanReview: user.Can(model.PermReviewQuestion, q.SubjectID),
		},
	}, nil
}

// canDelete 仅创建者本人、学科负责人或管理员可删除。
func (s *QuestionService) canDelete(user *model.User, q *model.Question) bool {
	if user.IsSuperuser || q.CreatedBy == user.ID {
		return true
	}
	return user.Can(model.PermManageSubject, q.SubjectID)
}

// Create 新建题目。
func (s *QuestionService) Create(ctx context.Context, user *model.User, req *schema.QuestionCreateRequest) (*schema.QuestionDetail, error) {
	if err := authorize(user, model.PermEditQuestion, req.SubjectID); err != nil {
		return nil, err
	}
	if _, err := s.repos.Subjects.Get(ctx, req.SubjectID); err != nil {
		return nil, err
	}

	prepared, err := s.prepare(ctx, req.SubjectID, req.ParentID, req.QuestionType, req.Stem,
		req.Options, req.Answer, req.TagIDs, req.KnowledgePointIDs)
	if err != nil {
		return nil, err
	}

	q := &model.Question{
		SubjectID:       req.SubjectID,
		ParentID:        req.ParentID,
		QuestionType:    req.QuestionType,
		Difficulty:      3,
		Status:          s.initialStatus(user, req.SubjectID, req.Status),
		Visibility:      model.VisibilityPublic,
		Stem:            req.Stem,
		Options:         prepared.optionsJSON,
		Answer:          prepared.answerJSON,
		Analysis:        req.Analysis,
		Solution:        req.Solution,
		Summary:         req.Summary,
		StemText:        prepared.stemText,
		ContentVersion:  1,
		ContentRevision: 1,
		Source:          req.Source,
		CreatedBy:       user.ID,
		UpdatedBy:       user.ID,
	}
	if req.Difficulty != nil {
		q.Difficulty = *req.Difficulty
	}
	if req.Visibility.Valid() {
		q.Visibility = req.Visibility
	}

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Questions.Create(ctx, q); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if err := s.repos.Questions.SetTags(ctx, q.ID, req.TagIDs); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.Questions.SetKnowledgePoints(ctx, q.ID, req.KnowledgePointIDs)
	})
	if err != nil {
		return nil, err
	}

	return s.Get(ctx, user, q.ID)
}

// initialStatus 决定新题目的初始状态。
//
// 拥有审核权限的人（学科负责人/管理员）录入即发布；
// 普通编辑录入的是草稿，需要走审核流程。
func (s *QuestionService) initialStatus(user *model.User, subjectID uint, requested model.QuestionStatus) model.QuestionStatus {
	if user.Can(model.PermReviewQuestion, subjectID) {
		if requested.Valid() {
			return requested
		}
		return model.QuestionStatusPublished
	}
	return model.QuestionStatusDraft
}

// prepared 校验与归一化后的结果。
type prepared struct {
	optionsJSON model.JSON
	answerJSON  model.JSON
	stemText    string
}

// prepare 执行写入前的统一处理：归一化选项 id、校验题型与答案一致性、
// 校验标签与知识点归属，并提取题干纯文本。
//
// 三条写入路径（HTTP、AI 抽取、批量导入）都走这里，因此不存在
// "从某个入口进来能绕过校验"的可能。
func (s *QuestionService) prepare(
	ctx context.Context,
	subjectID uint,
	parentID *uint,
	qtype model.QuestionType,
	stem model.JSON,
	optionInputs []schema.OptionInput,
	answer model.AnswerSpec,
	tagIDs, kpIDs []uint,
) (*prepared, error) {
	if stem.IsEmpty() {
		return nil, apperr.New(apperr.CodeInvalidParam, "题干不能为空")
	}

	if parentID != nil {
		parent, err := s.repos.Questions.Get(ctx, *parentID)
		if err != nil {
			return nil, err
		}
		if parent.SubjectID != subjectID {
			return nil, apperr.New(apperr.CodeCrossSubject, "母题不属于该学科")
		}
	}

	options := make([]model.Option, 0, len(optionInputs))
	for i := range optionInputs {
		options = append(options, model.Option{
			Label:   optionInputs[i].Label,
			Content: optionInputs[i].Content,
		})
	}
	normalized, err := domain.NormalizeOptions(options)
	if err != nil {
		return nil, err
	}

	var stemDoc domain.RichDoc
	if err := stem.Into(&stemDoc); err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "题干格式不正确").WithDetail("%v", err)
	}
	// 顺序很重要：先校验外形（题型/选项数量/答案种类），
	// 再把答案里的选项引用（标签或 id）解析为 id，最后做完整校验。
	// 反过来的话，"选择题一个选项都没给"会被报成"引用了不存在的选项"。
	if err := domain.ValidateShape(qtype, normalized, answer); err != nil {
		return nil, err
	}
	if err := domain.ResolveCorrectLabels(normalized, &answer); err != nil {
		return nil, err
	}
	if err := domain.ValidateQuestion(qtype, stemDoc, normalized, answer); err != nil {
		return nil, err
	}

	if err := s.checkTagOwnership(ctx, subjectID, tagIDs); err != nil {
		return nil, err
	}
	if err := s.checkKPOwnership(ctx, subjectID, kpIDs); err != nil {
		return nil, err
	}

	optionsJSON, err := model.JSONOf(normalized)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	answerJSON, err := model.JSONOf(answer)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	return &prepared{
		optionsJSON: optionsJSON,
		answerJSON:  answerJSON,
		stemText:    domain.PlainText(stemDoc),
	}, nil
}

// checkParentLink 校验把题目挂到 parentID 下不会形成环。
//
// 为什么必须校验：parent_id 是自引用外键，而级联软删靠 ChildIDs 逐层向下展开。
// 一旦 A→B→A 成环，那次展开就会在环上永远转下去——不是"删多了"，
// 而是请求再也返回不了。前端下拉只列独立题因此天然不成环，
// 但 API 是公开的，校验必须落在服务端。
func (s *QuestionService) checkParentLink(ctx context.Context, id, subjectID uint, parentID *uint) error {
	parent, err := s.repos.Questions.Get(ctx, *parentID)
	if err != nil {
		return err
	}
	if parent.SubjectID != subjectID {
		return apperr.New(apperr.CodeCrossSubject, "母题不属于该学科")
	}
	if parent.ID == id {
		return apperr.New(apperr.CodeInvalidParam, "题目不能挂到自己名下")
	}

	// 沿着候选母题向上走，看会不会绕回自己。
	// 深度上限与 ChildIDs 的展开上限一致：正常数据只有两层，
	// 走到上限仍未绕回就说明链上有一环（或脏数据），一律拒绝。
	const maxDepth = 10
	cursor := parent
	for depth := 0; depth < maxDepth; depth++ {
		if cursor.ParentID == nil {
			return nil
		}
		if *cursor.ParentID == id {
			return apperr.New(apperr.CodeInvalidParam, "不能挂到自己的子题下").
				WithDetail("母题 %d 的祖先链上存在本题 %d", parent.ID, id)
		}
		next, err := s.repos.Questions.Get(ctx, *cursor.ParentID)
		if err != nil {
			// 链断在这里说明上游数据已经不一致，如实报出来而不是当作"没问题"。
			return apperr.From(err)
		}
		cursor = next
	}
	return apperr.New(apperr.CodeConflict, "母题链层级过深或已存在环")
}

// resolveParentID 把请求里的 parent_id 归一化成"指针的指针"语义。
//
// 约定：JSON 里传 0 表示"解除挂载、变回独立题"，其他非零值表示挂到该题下。
// 前端 el-select 清空时正好给出 null（= 不传 = 不动）与 0 两种截然不同的含义，
// 这里把两者区分开。
func resolveParentID(raw uint) (*uint, error) {
	if raw == 0 {
		return nil, nil
	}
	return &raw, nil
}

// checkTagOwnership 校验标签都属于该学科，防止把 A 学科的标签挂到 B 学科的题目上。
func (s *QuestionService) checkTagOwnership(ctx context.Context, subjectID uint, tagIDs []uint) error {
	if len(tagIDs) == 0 {
		return nil
	}
	valid, err := s.repos.Tags.IDsBySubject(ctx, subjectID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	for _, id := range tagIDs {
		if !valid[id] {
			return apperr.New(apperr.CodeCrossSubject, "标签不属于该学科").WithDetail("tag_id=%d", id)
		}
	}
	return nil
}

func (s *QuestionService) checkKPOwnership(ctx context.Context, subjectID uint, kpIDs []uint) error {
	if len(kpIDs) == 0 {
		return nil
	}
	valid, err := s.repos.KnowledgePoints.IDsBySubject(ctx, subjectID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	for _, id := range kpIDs {
		if !valid[id] {
			return apperr.New(apperr.CodeCrossSubject, "知识点不属于该学科").WithDetail("kp_id=%d", id)
		}
	}
	return nil
}
