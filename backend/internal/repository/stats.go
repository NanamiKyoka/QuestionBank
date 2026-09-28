package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// StatsRepo 统计聚合查询。
//
// 单独一个仓储而不是散落在各自的仓储里：这些查询的共同点是
// "只读、只做聚合、字段组合跨多个领域"，放在一起便于对照与优化。
// 所有查询都走索引列（subject_id / status / question_type / created_at）。
type StatsRepo struct {
	db *gorm.DB
}

func NewStatsRepo(db *gorm.DB) *StatsRepo { return &StatsRepo{db: db} }

// CountRow 通用分组计数结果。
type CountRow struct {
	Key   string
	Total int64
}

// GroupCountQuestions 按某列分组统计题目数。
//
// 列名由调用方从固定枚举里传，不接受用户输入，因此不存在注入风险。
func (r *StatsRepo) GroupCountQuestions(ctx context.Context, subjectID uint, column string) ([]CountRow, error) {
	q := conn(ctx, r.db).Model(&model.Question{}).
		Select(column + " AS key, COUNT(*) AS total").
		Group(column).
		Order("total DESC")
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}

	var rows []CountRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountQuestions 题目总数。subjectID 为 0 时统计全库。
func (r *StatsRepo) CountQuestions(ctx context.Context, subjectID uint) (int64, error) {
	q := conn(ctx, r.db).Model(&model.Question{})
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

// TrendRow 按日期的题量趋势。
type TrendRow struct {
	Day   string
	Total int64
}

// QuestionTrend 返回最近 days 天每天的建题数量。
//
// 两个方言的日期函数差异很大（SQLite 是 date('now','-30 days')，
// MySQL 是 DATE_SUB(NOW(), INTERVAL 30 DAY)），因此**下界在 Go 里算好
// 再作为参数传进去**——只有 DATE(created_at) 这一处用到方言函数，
// 而它在两边都可用。这样一份查询代码能同时跑两种库。
//
// 注意只会返回"有数据的那些天"。前端画趋势图时需要自行补齐空缺日期，
// 这里不补零是为了避免返回一堆无意义的行。
func (r *StatsRepo) QuestionTrend(ctx context.Context, subjectID uint, days int) ([]TrendRow, error) {
	if days <= 0 || days > 365 {
		days = 30
	}

	q := conn(ctx, r.db).Model(&model.Question{}).
		Select("DATE(created_at) AS day, COUNT(*) AS total").
		Where("created_at >= ?", time.Now().UTC().AddDate(0, 0, -days)).
		Group("DATE(created_at)").
		Order("day ASC")
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}

	var rows []TrendRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountKnowledgePoints 知识点总数。
func (r *StatsRepo) CountKnowledgePoints(ctx context.Context, subjectID uint) (int64, error) {
	q := conn(ctx, r.db).Model(&model.KnowledgePoint{})
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

// CountTags 标签总数。
func (r *StatsRepo) CountTags(ctx context.Context, subjectID uint) (int64, error) {
	q := conn(ctx, r.db).Model(&model.Tag{})
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

// CountPapers 试卷总数。
func (r *StatsRepo) CountPapers(ctx context.Context, subjectID uint) (int64, error) {
	q := conn(ctx, r.db).Model(&model.Paper{})
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

// GroupCountPapersByStatus 按状态统计试卷。
func (r *StatsRepo) GroupCountPapersByStatus(ctx context.Context, subjectID uint) ([]CountRow, error) {
	q := conn(ctx, r.db).Model(&model.Paper{}).
		Select("status AS key, COUNT(*) AS total").
		Group("status")
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}
	var rows []CountRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountSubjects 学科总数。
func (r *StatsRepo) CountSubjects(ctx context.Context) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.Subject{}).Count(&n).Error
	return n, err
}

// CountUsers 用户总数。
func (r *StatsRepo) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.User{}).Count(&n).Error
	return n, err
}

// AIUsage AI 用量汇总。用于回答"这个月花了多少 token"。
type AIUsage struct {
	Runs             int64
	PromptTokens     int64
	CompletionTokens int64
}

// AgentUsage 汇总 Agent 调用的次数与 token 用量。
//
// 用 COALESCE 包住 SUM：没有任何运行记录时 SUM 返回 NULL，
// 直接扫描进 int64 会报错而不是得到 0。
func (r *StatsRepo) AgentUsage(ctx context.Context, sinceDays int) (AIUsage, error) {
	var usage AIUsage

	q := conn(ctx, r.db).Model(&model.AgentRun{}).
		Select("COUNT(*) AS runs, " +
			"COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, " +
			"COALESCE(SUM(completion_tokens), 0) AS completion_tokens")
	if sinceDays > 0 {
		q = q.Where("created_at >= ?", time.Now().UTC().AddDate(0, 0, -sinceDays))
	}

	if err := q.Scan(&usage).Error; err != nil {
		return usage, err
	}
	return usage, nil
}

// CountImports 导入任务统计。
func (r *StatsRepo) CountImports(ctx context.Context, subjectID uint) (total int64, saved int64, err error) {
	type row struct {
		Total int64
		Saved int64
	}
	var out row

	q := conn(ctx, r.db).Model(&model.ImportTask{}).
		Select("COUNT(*) AS total, COALESCE(SUM(saved_count), 0) AS saved")
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}
	if err := q.Scan(&out).Error; err != nil {
		return 0, 0, err
	}
	return out.Total, out.Saved, nil
}

// RecentPaper 最近更新的试卷，用于仪表盘的"最近动态"。
type RecentPaper struct {
	ID        uint
	Title     string
	Status    model.PaperStatus
	UpdatedAt string
}

// RecentPapers 返回最近更新的若干份试卷。
func (r *StatsRepo) RecentPapers(ctx context.Context, subjectID uint, limit int) ([]RecentPaper, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	q := conn(ctx, r.db).Model(&model.Paper{}).
		Select("id, title, status, updated_at")
	if subjectID > 0 {
		q = q.Where("subject_id = ?", subjectID)
	}

	var rows []RecentPaper
	if err := q.Order("updated_at DESC, id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ActivityRow 审计日志的聚合行。
type ActivityRow struct {
	Action string
	Total  int64
}

// GroupActivityByAction 按动作类型统计操作次数，用于"最近都在做什么"。
func (r *StatsRepo) GroupActivityByAction(ctx context.Context, sinceDays int, limit int) ([]ActivityRow, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	q := conn(ctx, r.db).Model(&model.ActivityLog{}).
		Select("action, COUNT(*) AS total").
		Group("action").
		Order("total DESC").
		Limit(limit)
	if sinceDays > 0 {
		q = q.Where("created_at >= ?", time.Now().UTC().AddDate(0, 0, -sinceDays))
	}

	var rows []ActivityRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
