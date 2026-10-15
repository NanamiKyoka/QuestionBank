package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// QuestionRepo 题目数据访问。
type QuestionRepo struct {
	db *gorm.DB
}

func NewQuestionRepo(db *gorm.DB) *QuestionRepo { return &QuestionRepo{db: db} }

// ---- 写入 ----

func (r *QuestionRepo) Create(ctx context.Context, q *model.Question) error {
	if err := conn(ctx, r.db).Create(q).Error; err != nil {
		return fmt.Errorf("创建题目失败: %w", err)
	}
	return nil
}

// UpdateFields 按字段白名单更新题目。
func (r *QuestionRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	res := conn(ctx, r.db).Model(&model.Question{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("更新题目失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		if _, err := r.Get(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// BumpContentRevision 内容修订号 +1。
//
// 试卷节点记录着它引用的题目的 content_revision，据此判断"是否需要同步最新内容"。
// 因此只有**影响题目呈现**的字段变更才允许调用本方法；
// 改来源、改可见性不应该触发，否则会让满屏试卷误报"题目已更新"。
func (r *QuestionRepo) BumpContentRevision(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Model(&model.Question{}).
		Where("id = ?", id).
		UpdateColumn("content_revision", gorm.Expr("content_revision + 1")).Error
}

// SetTags 覆盖式设置题目标签（先清空再写入）。
func (r *QuestionRepo) SetTags(ctx context.Context, questionID uint, tagIDs []uint) error {
	db := conn(ctx, r.db)
	if err := db.Where("question_id = ?", questionID).Delete(&model.QuestionTag{}).Error; err != nil {
		return err
	}
	if len(tagIDs) == 0 {
		return nil
	}
	links := make([]model.QuestionTag, 0, len(tagIDs))
	for _, id := range tagIDs {
		links = append(links, model.QuestionTag{QuestionID: questionID, TagID: id})
	}
	return db.Create(&links).Error
}

// SetKnowledgePoints 覆盖式设置题目知识点。
func (r *QuestionRepo) SetKnowledgePoints(ctx context.Context, questionID uint, kpIDs []uint) error {
	db := conn(ctx, r.db)
	if err := db.Where("question_id = ?", questionID).Delete(&model.QuestionKnowledgePoint{}).Error; err != nil {
		return err
	}
	if len(kpIDs) == 0 {
		return nil
	}
	links := make([]model.QuestionKnowledgePoint, 0, len(kpIDs))
	for _, id := range kpIDs {
		links = append(links, model.QuestionKnowledgePoint{QuestionID: questionID, KnowledgePointID: id})
	}
	return db.Create(&links).Error
}

// SoftDelete 批量软删除。
func (r *QuestionRepo) SoftDelete(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return conn(ctx, r.db).Where("id IN ?", ids).Delete(&model.Question{}).Error
}

// Restore 从回收站恢复。
func (r *QuestionRepo) Restore(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return conn(ctx, r.db).Unscoped().
		Model(&model.Question{}).
		Where("id IN ?", ids).
		Update("deleted_at", nil).Error
}

// ---- 读取 ----

// Get 读取题目（不含关联）。
func (r *QuestionRepo) Get(ctx context.Context, id uint) (*model.Question, error) {
	var q model.Question
	if err := conn(ctx, r.db).First(&q, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("题目 %d 不存在", id)
		}
		return nil, err
	}
	return &q, nil
}

// GetDeleted 读取已软删除的题目。
//
// 恢复流程必须走这里：普通 Get 会被 GORM 自动追加 deleted_at IS NULL，
// 于是"恢复一道已删除的题目"永远查不到记录、直接 404——
// 路由上明明挂着 POST /questions/:id/restore，实际却永远失败。
func (r *QuestionRepo) GetDeleted(ctx context.Context, id uint) (*model.Question, error) {
	var q model.Question
	if err := conn(ctx, r.db).Unscoped().First(&q, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("题目 %d 不存在", id)
		}
		return nil, err
	}
	if !q.DeletedAt.Valid {
		return nil, apperr.ErrUnprocessable.WithDetail("题目 %d 未被删除", id)
	}
	return &q, nil
}

// List 按条件查询题目。
func (r *QuestionRepo) List(ctx context.Context, f model.QuestionFilter, sort model.QuestionSort, limit, offset int) ([]model.Question, int64, error) {
	return r.list(ctx, r.buildQuery(ctx, f), sort, limit, offset)
}

// ListByImportTask 列出某次导入产生的题目。
//
// 复用 List 的**全部**筛选与可见性条件，只多一个 import_task_id 条件。
// 刻意不另写一套 SQL：两边条件一旦不同源，就会出现
// "历史页说这次导入了 20 道、按同一条件在题库页只搜到 18 道"这种对不上账的情况。
func (r *QuestionRepo) ListByImportTask(ctx context.Context, f model.QuestionFilter, importTaskID uint, sort model.QuestionSort, limit, offset int) ([]model.Question, int64, error) {
	return r.list(ctx, r.buildQuery(ctx, f).Where("questions.import_task_id = ?", importTaskID), sort, limit, offset)
}

// list 分页执行已构造好的查询。List 与 ListByImportTask 共用，
// 保证"计数"与"取数"永远来自同一条 SQL。
func (r *QuestionRepo) list(ctx context.Context, q *gorm.DB, sort model.QuestionSort, limit, offset int) ([]model.Question, int64, error) {
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计题目数量失败: %w", err)
	}
	if total == 0 {
		return []model.Question{}, 0, nil
	}

	var list []model.Question
	// 排序子句来自受控的枚举，不含用户输入，不存在注入风险。
	err := q.Order(sort.Normalize().OrderClause()).
		Limit(limit).Offset(offset).
		Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// SetImportTask 批量把题目挂到导入任务上（一条 UPDATE 覆盖整批）。
//
// 导入入库后要做一次溯源回填：题目必须先建出来才有主键，
// 而同一批题目共享同一个导入任务 id，因此一次 IN 更新即可，
// 不必在循环里给每道题补一条 UPDATE——200 道题的导入会因此少 200 次往返。
func (r *QuestionRepo) SetImportTask(ctx context.Context, ids []uint, importTaskID uint) error {
	if len(ids) == 0 || importTaskID == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.Question{}).
		Where("id IN ?", ids).
		Update("import_task_id", importTaskID).Error
}

// buildQuery 构造筛选条件。列表与计数复用，保证两者结果一致。
func (r *QuestionRepo) buildQuery(ctx context.Context, f model.QuestionFilter) *gorm.DB {
	q := conn(ctx, r.db).Model(&model.Question{})

	// 单靠 IncludeDeleted 只能得到"活的 + 删掉的"混在一起的结果，
	// 而 SoftBase.DeletedAt 标记了 json:"-"，客户端没有任何字段能把两者区分开。
	// 回收站要的是"只给我删掉的"，所以另给一个互斥开关。
	if f.OnlyDeleted {
		q = q.Unscoped().Where("questions.deleted_at IS NOT NULL")
	} else if f.IncludeDeleted {
		q = q.Unscoped()
	}
	if f.SubjectID > 0 {
		q = q.Where("questions.subject_id = ?", f.SubjectID)
	}

	if f.Keyword != "" {
		q = q.Where("questions.stem_text LIKE ?", "%"+escapeLike(f.Keyword)+"%")
	}
	if f.Type != "" {
		q = q.Where("questions.question_type = ?", f.Type)
	}
	if f.Status != "" {
		q = q.Where("questions.status = ?", f.Status)
	}
	if f.Difficulty > 0 {
		q = q.Where("questions.difficulty = ?", f.Difficulty)
	} else {
		if f.DifficultyMin > 0 {
			q = q.Where("questions.difficulty >= ?", f.DifficultyMin)
		}
		if f.DifficultyMax > 0 {
			q = q.Where("questions.difficulty <= ?", f.DifficultyMax)
		}
	}
	if f.CreatedBy > 0 {
		q = q.Where("questions.created_by = ?", f.CreatedBy)
	}
	if f.Source != "" {
		q = q.Where("questions.source LIKE ?", "%"+escapeLike(f.Source)+"%")
	}
	if f.ReviewCount != nil {
		q = q.Where("questions.review_count = ?", *f.ReviewCount)
	}
	if f.ReviewerID > 0 {
		// "被某人审核过"只能通过审核流水判定，questions 表上没有这一列。
		sub := conn(ctx, r.db).Model(&model.ReviewLog{}).
			Select("question_id").
			Where("reviewer_id = ?", f.ReviewerID)
		q = q.Where("questions.id IN (?)", sub)
	}
	if f.RootOnly {
		q = q.Where("questions.parent_id IS NULL")
	}

	// 可见性：私有题仅创建者可见，除非拥有查看他人私有题的权限。
	if !f.CanViewPrivateAll {
		q = q.Where("(questions.visibility = ? OR questions.created_by = ?)",
			model.VisibilityPublic, f.ViewerID)
	}

	if len(f.KnowledgePointIDs) > 0 {
		sub := conn(ctx, r.db).Model(&model.QuestionKnowledgePoint{}).
			Select("question_id").
			Where("knowledge_point_id IN ?", f.KnowledgePointIDs)
		q = q.Where("questions.id IN (?)", sub)
	}

	switch {
	case len(f.TagIDs) == 1:
		sub := conn(ctx, r.db).Model(&model.QuestionTag{}).
			Select("question_id").
			Where("tag_id = ?", f.TagIDs[0])
		q = q.Where("questions.id IN (?)", sub)
	case len(f.TagIDs) > 1 && f.TagMatch == "and":
		// 同时具备全部标签：分组后要求命中数等于标签数。
		sub := conn(ctx, r.db).Model(&model.QuestionTag{}).
			Select("question_id").
			Where("tag_id IN ?", f.TagIDs).
			Group("question_id").
			Having("COUNT(DISTINCT tag_id) = ?", len(f.TagIDs))
		q = q.Where("questions.id IN (?)", sub)
	case len(f.TagIDs) > 1:
		sub := conn(ctx, r.db).Model(&model.QuestionTag{}).
			Select("question_id").
			Where("tag_id IN ?", f.TagIDs)
		q = q.Where("questions.id IN (?)", sub)
	}

	return q
}

// ListByIDs 按 id 集合批量取题目，用于试卷内容投影。
func (r *QuestionRepo) ListByIDs(ctx context.Context, ids []uint) ([]model.Question, error) {
	if len(ids) == 0 {
		return []model.Question{}, nil
	}
	var out []model.Question
	err := conn(ctx, r.db).Where("id IN ?", ids).Find(&out).Error
	return out, err
}

// ExistingIDs 返回这些 id 中真实存在（含已软删除）的集合。
//
// 用 Unscoped 是刻意的：试卷里引用的题目若被删除，我们要能区分
// "题目被删了"与"题目从来不存在"——前者提示"引用已失效"，
// 后者说明数据被外部改过。
func (r *QuestionRepo) ExistingIDs(ctx context.Context, ids []uint) (map[uint]int64, error) {
	out := make(map[uint]int64)
	if len(ids) == 0 {
		return out, nil
	}
	type row struct {
		ID              uint
		ContentRevision int64
	}
	var rows []row
	err := conn(ctx, r.db).Unscoped().Model(&model.Question{}).
		Select("id, content_revision").
		Where("id IN ?", ids).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.ContentRevision
	}
	return out, nil
}

// ChildIDs 返回这些题目的全部子孙 id（含自身），用于级联软删除。
func (r *QuestionRepo) ChildIDs(ctx context.Context, rootIDs []uint) ([]uint, error) {
	if len(rootIDs) == 0 {
		return nil, nil
	}
	all := make([]uint, 0, len(rootIDs))
	all = append(all, rootIDs...)

	frontier := rootIDs
	// 题目的层级通常只有两层（母题/子题），但这里按通用写法逐层展开，
	// 避免将来支持多层结构时出现"删了母题留下孙子题"的孤儿数据。
	for depth := 0; depth < 10 && len(frontier) > 0; depth++ {
		var next []uint
		err := conn(ctx, r.db).Model(&model.Question{}).
			Where("parent_id IN ?", frontier).
			Pluck("id", &next).Error
		if err != nil {
			return nil, err
		}
		if len(next) == 0 {
			break
		}
		all = append(all, next...)
		frontier = next
	}
	return all, nil
}

// ExistsInSubject 校验这些题目是否都属于指定学科。
// 返回不属于该学科的 id 列表。
func (r *QuestionRepo) ExistsInSubject(ctx context.Context, ids []uint, subjectID uint) ([]uint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var found []uint
	err := conn(ctx, r.db).Model(&model.Question{}).
		Where("id IN ? AND subject_id = ?", ids, subjectID).
		Pluck("id", &found).Error
	if err != nil {
		return nil, err
	}
	set := make(map[uint]bool, len(found))
	for _, id := range found {
		set[id] = true
	}
	var missing []uint
	for _, id := range ids {
		if !set[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// ---- 关联批量装载（避免 N+1）----

// TagsForQuestions 一次性取出多道题的标签。
// 无论题目有多少条，都只发一条 SQL——这正是参考实现里被查询放大拖慢的地方。
func (r *QuestionRepo) TagsForQuestions(ctx context.Context, questionIDs []uint) (map[uint][]model.Tag, error) {
	out := make(map[uint][]model.Tag)
	if len(questionIDs) == 0 {
		return out, nil
	}

	type row struct {
		QuestionID uint
		TagID      uint
		Name       string
		Color      string
	}
	var rows []row
	err := conn(ctx, r.db).
		Table("question_tags AS qt").
		Select("qt.question_id AS question_id, t.id AS tag_id, t.name AS name, t.color AS color").
		Joins("JOIN tags AS t ON t.id = qt.tag_id AND t.deleted_at IS NULL").
		Where("qt.question_id IN ?", questionIDs).
		Order("t.sort_order ASC, t.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, r := range rows {
		out[r.QuestionID] = append(out[r.QuestionID], model.Tag{
			Base:  model.Base{ID: r.TagID},
			Name:  r.Name,
			Color: r.Color,
		})
	}
	return out, nil
}

// KnowledgePointsForQuestions 一次性取出多道题的知识点。
func (r *QuestionRepo) KnowledgePointsForQuestions(ctx context.Context, questionIDs []uint) (map[uint][]model.KnowledgePoint, error) {
	out := make(map[uint][]model.KnowledgePoint)
	if len(questionIDs) == 0 {
		return out, nil
	}

	type row struct {
		QuestionID uint
		KPID       uint
		Name       string
		Path       string
	}
	var rows []row
	err := conn(ctx, r.db).
		Table("question_knowledge_points AS qkp").
		Select("qkp.question_id AS question_id, kp.id AS kp_id, kp.name AS name, kp.path AS path").
		Joins("JOIN knowledge_points AS kp ON kp.id = qkp.knowledge_point_id AND kp.deleted_at IS NULL").
		Where("qkp.question_id IN ?", questionIDs).
		Order("kp.path ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, r := range rows {
		out[r.QuestionID] = append(out[r.QuestionID], model.KnowledgePoint{
			Base: model.Base{ID: r.KPID},
			Name: r.Name,
			Path: r.Path,
		})
	}
	return out, nil
}

// ChildCounts 批量统计子题数量。
func (r *QuestionRepo) ChildCounts(ctx context.Context, parentIDs []uint) (map[uint]int, error) {
	out := make(map[uint]int)
	if len(parentIDs) == 0 {
		return out, nil
	}

	type row struct {
		ParentID uint
		Total    int
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.Question{}).
		Select("parent_id, COUNT(*) AS total").
		Where("parent_id IN ?", parentIDs).
		Group("parent_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ParentID] = r.Total
	}
	return out, nil
}

// Children 返回直接子题。
func (r *QuestionRepo) Children(ctx context.Context, parentID uint) ([]model.Question, error) {
	var out []model.Question
	err := conn(ctx, r.db).Where("parent_id = ?", parentID).Order("id ASC").Find(&out).Error
	return out, err
}

// ---- 审核 ----

// AddReviewLog 记录一次审核动作。
func (r *QuestionRepo) AddReviewLog(ctx context.Context, log *model.ReviewLog) error {
	return conn(ctx, r.db).Create(log).Error
}

// ListReviewLogs 返回题目的审核日志。
func (r *QuestionRepo) ListReviewLogs(ctx context.Context, questionID uint, limit int) ([]model.ReviewLog, error) {
	var out []model.ReviewLog
	err := conn(ctx, r.db).
		Preload("Reviewer").
		Where("question_id = ?", questionID).
		Order("id DESC").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// IncrementReviewCount 审核通过次数 +1。
func (r *QuestionRepo) IncrementReviewCount(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Model(&model.Question{}).
		Where("id = ?", id).
		UpdateColumn("review_count", gorm.Expr("review_count + 1")).Error
}

// StatusCounts 统计学科下各状态的题目数量，供仪表盘使用。
func (r *QuestionRepo) StatusCounts(ctx context.Context, subjectID uint) (map[model.QuestionStatus]int64, error) {
	type row struct {
		Status model.QuestionStatus
		Total  int64
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.Question{}).
		Select("status, COUNT(*) AS total").
		Where("subject_id = ?", subjectID).
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[model.QuestionStatus]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Total
	}
	return out, nil
}

// TypeCounts 按题型统计。
func (r *QuestionRepo) TypeCounts(ctx context.Context, subjectID uint) (map[model.QuestionType]int64, error) {
	type row struct {
		QuestionType model.QuestionType
		Total        int64
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.Question{}).
		Select("question_type, COUNT(*) AS total").
		Where("subject_id = ?", subjectID).
		Group("question_type").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[model.QuestionType]int64, len(rows))
	for _, r := range rows {
		out[r.QuestionType] = r.Total
	}
	return out, nil
}
