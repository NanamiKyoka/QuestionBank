package service

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// PlanQuestions builds a deterministic candidate selection for a paper section.
func (s *QuestionService) PlanQuestions(
	ctx context.Context,
	user *model.User,
	subjectID uint,
	req *schema.PaperPlanRequest,
) (*schema.PaperPlanResponse, error) {
	if req.Count < 1 || req.Count > 100 {
		return nil, apperr.New(apperr.CodeInvalidParam, "题目数量必须在 1 到 100 之间")
	}
	minDifficulty := req.DifficultyMin
	maxDifficulty := req.DifficultyMax
	if minDifficulty == 0 {
		minDifficulty = 1
	}
	if maxDifficulty == 0 {
		maxDifficulty = 5
	}
	if minDifficulty > maxDifficulty {
		return nil, apperr.New(apperr.CodeInvalidParam, "最低难度不能高于最高难度")
	}

	limit := req.Count * 6
	if limit < 100 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	briefs, _, err := s.List(ctx, user, subjectID, ListQuery{
		Type:              req.QuestionType,
		Status:            model.QuestionStatusPublished,
		DifficultyMin:     minDifficulty,
		DifficultyMax:     maxDifficulty,
		KnowledgePointIDs: req.KnowledgePointIDs,
		IncludeSub:        req.IncludeSub,
		TagIDs:            req.TagIDs,
		Source:            req.Source,
		RootOnly:          true,
		Sort:              model.SortByCreatedDesc,
	}, limit, 0)
	if err != nil {
		return nil, err
	}

	excluded := make(map[uint]struct{}, len(req.ExcludeQuestionIDs))
	for _, id := range req.ExcludeQuestionIDs {
		excluded[id] = struct{}{}
	}
	candidates := make([]schema.QuestionBrief, 0, len(briefs))
	for i := range briefs {
		if _, skip := excluded[briefs[i].ID]; skip {
			continue
		}
		candidates = append(candidates, briefs[i])
	}

	seed := req.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	buckets := make([][]schema.QuestionBrief, 6)
	for i := range candidates {
		d := candidates[i].Difficulty
		if d < 1 || d > 5 {
			d = 3
		}
		buckets[d] = append(buckets[d], candidates[i])
	}
	for d := minDifficulty; d <= maxDifficulty; d++ {
		sort.SliceStable(buckets[d], func(i, j int) bool {
			if buckets[d][i].ReviewCount != buckets[d][j].ReviewCount {
				return buckets[d][i].ReviewCount > buckets[d][j].ReviewCount
			}
			return false
		})
	}

	selected := make([]schema.QuestionBrief, 0, req.Count)
	for len(selected) < req.Count {
		added := false
		for d := minDifficulty; d <= maxDifficulty && len(selected) < req.Count; d++ {
			if len(buckets[d]) == 0 {
				continue
			}
			selected = append(selected, buckets[d][0])
			buckets[d] = buckets[d][1:]
			added = true
		}
		if !added {
			break
		}
	}

	counts := make(map[int]int)
	for i := range selected {
		counts[selected[i].Difficulty]++
	}
	warnings := make([]string, 0, 1)
	if len(selected) < req.Count {
		warnings = append(warnings, fmt.Sprintf("当前条件下只有 %d 道可用题，少于需要的 %d 道", len(selected), req.Count))
	}
	return &schema.PaperPlanResponse{
		Requested:        req.Count,
		Available:        len(candidates),
		Selected:         len(selected),
		Missing:          req.Count - len(selected),
		Items:            selected,
		DifficultyCounts: counts,
		Warnings:         warnings,
	}, nil
}
