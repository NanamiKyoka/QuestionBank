package service

import (
	"context"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// trendDays 趋势图的默认时间窗口。
const trendDays = 30

// aiUsageDays AI 用量的统计窗口。
const aiUsageDays = 30

// StatsService 统计概览。
type StatsService struct {
	repos *Repos
}

func NewStatsService(repos *Repos) *StatsService { return &StatsService{repos: repos} }

// Subject 返回学科维度的统计。
func (s *StatsService) Subject(ctx context.Context, user *model.User, subjectID uint) (*schema.SubjectStatsResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	subject, err := s.repos.Subjects.Get(ctx, subjectID)
	if err != nil {
		return nil, err
	}

	resp := &schema.SubjectStatsResponse{
		SubjectID:      subjectID,
		SubjectName:    subject.Name,
		PapersByStatus: map[string]int64{},
		QuestionTrend:  []schema.TrendPoint{},
		RecentPapers:   []schema.RecentPaperItem{},
	}

	questions, err := s.questionStats(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	resp.Questions = questions

	if resp.KnowledgePoints, err = s.repos.Stats.CountKnowledgePoints(ctx, subjectID); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.Tags, err = s.repos.Stats.CountTags(ctx, subjectID); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.Papers, err = s.repos.Stats.CountPapers(ctx, subjectID); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	paperStatus, err := s.repos.Stats.GroupCountPapersByStatus(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	for _, row := range paperStatus {
		resp.PapersByStatus[row.Key] = row.Total
	}

	if resp.ImportTotal, resp.ImportSaved, err = s.repos.Stats.CountImports(ctx, subjectID); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	trend, err := s.repos.Stats.QuestionTrend(ctx, subjectID, trendDays)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	for _, row := range trend {
		resp.QuestionTrend = append(resp.QuestionTrend, schema.TrendPoint{Day: row.Day, Total: row.Total})
	}

	recent, err := s.repos.Stats.RecentPapers(ctx, subjectID, 5)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	for _, r := range recent {
		resp.RecentPapers = append(resp.RecentPapers, schema.RecentPaperItem{
			ID:        r.ID,
			Title:     r.Title,
			Status:    string(r.Status),
			UpdatedAt: r.UpdatedAt,
		})
	}

	return resp, nil
}

// questionStats 汇总题目统计。
func (s *StatsService) questionStats(ctx context.Context, subjectID uint) (schema.QuestionStats, error) {
	stats := schema.QuestionStats{
		ByType:       map[string]int64{},
		ByDifficulty: map[string]int64{},
	}

	total, err := s.repos.Stats.CountQuestions(ctx, subjectID)
	if err != nil {
		return stats, apperr.ErrInternal.WithCause(err)
	}
	stats.Total = total

	byStatus, err := s.repos.Stats.GroupCountQuestions(ctx, subjectID, "status")
	if err != nil {
		return stats, apperr.ErrInternal.WithCause(err)
	}
	for _, row := range byStatus {
		switch model.QuestionStatus(row.Key) {
		case model.QuestionStatusDraft:
			stats.Draft = row.Total
		case model.QuestionStatusPending:
			stats.Pending = row.Total
		case model.QuestionStatusPublished:
			stats.Published = row.Total
		case model.QuestionStatusArchived:
			stats.Archived = row.Total
		}
	}

	byType, err := s.repos.Stats.GroupCountQuestions(ctx, subjectID, "question_type")
	if err != nil {
		return stats, apperr.ErrInternal.WithCause(err)
	}
	for _, row := range byType {
		stats.ByType[row.Key] = row.Total
	}

	byDifficulty, err := s.repos.Stats.GroupCountQuestions(ctx, subjectID, "difficulty")
	if err != nil {
		return stats, apperr.ErrInternal.WithCause(err)
	}
	for _, row := range byDifficulty {
		stats.ByDifficulty[row.Key] = row.Total
	}

	return stats, nil
}

// Global 返回全局统计。
//
// 仅管理员：里面包含用户数与 AI token 用量，属于运营信息。
func (s *StatsService) Global(ctx context.Context, user *model.User) (*schema.GlobalStatsResponse, error) {
	if err := authorizeSuperuser(user); err != nil {
		return nil, err
	}

	resp := &schema.GlobalStatsResponse{TopActions: []schema.ActionCount{}}

	var err error
	if resp.Subjects, err = s.repos.Stats.CountSubjects(ctx); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.Questions, err = s.repos.Stats.CountQuestions(ctx, 0); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.KnowledgePoints, err = s.repos.Stats.CountKnowledgePoints(ctx, 0); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.Tags, err = s.repos.Stats.CountTags(ctx, 0); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.Papers, err = s.repos.Stats.CountPapers(ctx, 0); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.Users, err = s.repos.Stats.CountUsers(ctx); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	usage, err := s.repos.Stats.AgentUsage(ctx, aiUsageDays)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp.AI = schema.AIUsageStats{
		Runs:             usage.Runs,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.PromptTokens + usage.CompletionTokens,
	}

	actions, err := s.repos.Stats.GroupActivityByAction(ctx, aiUsageDays, 8)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	for _, a := range actions {
		resp.TopActions = append(resp.TopActions, schema.ActionCount{Action: a.Action, Total: a.Total})
	}

	return resp, nil
}
