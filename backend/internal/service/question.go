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
	Keyword    string
	Type       model.QuestionType
	Status     model.QuestionStatus
	Difficulty int

	KnowledgePointID uint
	IncludeSub       bool
	TagIDs           []uint
	TagMatch         string

	CreatedBy      uint
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
		TagIDs:            q.TagIDs,
		TagMatch:          q.TagMatch,
		CreatedBy:         q.CreatedBy,
		RootOnly:          q.RootOnly,
		IncludeDeleted:    q.IncludeDeleted,
		OnlyDeleted:       q.OnlyDeleted,
		ViewerID:          user.ID,
		CanViewPrivateAll: user.Can(model.PermViewPrivateAny, subjectID),
	}

	if q.KnowledgePointID > 0 {
		ids, err := s.resolveKnowledgePoints(ctx, q.KnowledgePointID, q.IncludeSub)
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

// resolveKnowledgePoints 把"知识点 + 是否含子树"解析为 id 集合。
func (s *QuestionService) resolveKnowledgePoints(ctx context.Context, kpID uint, includeSub bool) ([]uint, error) {
	kp, err := s.repos.KnowledgePoints.Get(ctx, kpID)
	if err != nil {
		return nil, err
	}
	if !includeSub {
		return []uint{kp.ID}, nil
	}
	subtree, err := s.repos.KnowledgePoints.Subtree(ctx, kp.SubjectID, kp.Path)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	ids := make([]uint, 0, len(subtree))
	for i := range subtree {
		ids = append(ids, subtree[i].ID)
	}
	return ids, nil
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

	var options []model.Option
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
