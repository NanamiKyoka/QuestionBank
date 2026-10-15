package service

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

type blueprintCandidate struct {
	question schema.QuestionBrief
	random   float64
}

type blueprintSectionState struct {
	spec     schema.PaperBlueprintSection
	coverage map[uint]struct{}
	sources  map[string]int
}

// PreviewBlueprint selects questions against a multi-section exam blueprint.
func (s *QuestionService) PreviewBlueprint(
	ctx context.Context,
	user *model.User,
	subjectID uint,
	req *schema.PaperBlueprintRequest,
) (*schema.PaperBlueprintResponse, error) {
	if len(req.Sections) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "至少需要一个题型分组")
	}

	totalRequested := 0
	seenKeys := make(map[string]struct{}, len(req.Sections))
	for i := range req.Sections {
		section := &req.Sections[i]
		if section.Count < 1 || section.Count > 100 {
			return nil, apperr.New(apperr.CodeInvalidParam, "每个分组的题目数量必须在 1 到 100 之间")
		}
		totalRequested += section.Count
		if totalRequested > 100 {
			return nil, apperr.New(apperr.CodeInvalidParam, "整卷预选最多 100 道题")
		}
		if section.Key == "" {
			section.Key = fmt.Sprintf("section-%d", i+1)
		}
		if _, exists := seenKeys[section.Key]; exists {
			return nil, apperr.New(apperr.CodeInvalidParam, "分组 key 不能重复")
		}
		seenKeys[section.Key] = struct{}{}
		if section.Title == "" {
			section.Title = fmt.Sprintf("第 %d 组", i+1)
		}
		if section.DifficultyMin == 0 {
			section.DifficultyMin = 1
		}
		if section.DifficultyMax == 0 {
			section.DifficultyMax = 5
		}
		if section.DifficultyMin > section.DifficultyMax {
			return nil, apperr.New(apperr.CodeInvalidParam, "最低难度不能高于最高难度")
		}
		if section.Score < 0 || section.Score > 1000 {
			return nil, apperr.New(apperr.CodeInvalidParam, "单题分值必须在 0 到 1000 之间")
		}
		targetTotal := 0
		for difficulty, count := range section.DifficultyCounts {
			if difficulty < section.DifficultyMin || difficulty > section.DifficultyMax || count < 0 {
				return nil, apperr.New(apperr.CodeInvalidParam, "难度配额超出了分组难度范围")
			}
			targetTotal += count
		}
		if targetTotal > section.Count {
			return nil, apperr.New(apperr.CodeInvalidParam, "难度配额总数不能超过分组题目数量")
		}
	}

	seed := req.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(seed))
	excluded := make(map[uint]struct{}, len(req.ExcludeQuestionIDs))
	for _, id := range req.ExcludeQuestionIDs {
		excluded[id] = struct{}{}
	}

	allItems := make([]schema.PaperBlueprintItem, 0, totalRequested)
	sectionResults := make([]schema.PaperBlueprintSectionResult, 0, len(req.Sections))
	warnings := make([]string, 0)
	for i := range req.Sections {
		state := &blueprintSectionState{
			spec:     req.Sections[i],
			coverage: make(map[uint]struct{}),
			sources:  make(map[string]int),
		}
		items, actualCounts, sectionWarnings, err := s.planBlueprintSection(ctx, user, subjectID, state, excluded, rng)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			excluded[item.ID] = struct{}{}
			allItems = append(allItems, item)
		}
		warnings = append(warnings, sectionWarnings...)
		sectionResults = append(sectionResults, schema.PaperBlueprintSectionResult{
			Key:              state.spec.Key,
			Title:            state.spec.Title,
			Requested:        state.spec.Count,
			Selected:         len(items),
			Missing:          state.spec.Count - len(items),
			DifficultyCounts: actualCounts,
		})
	}

	return &schema.PaperBlueprintResponse{
		Requested: totalRequested,
		Selected:  len(allItems),
		Missing:   totalRequested - len(allItems),
		Items:     allItems,
		Sections:  sectionResults,
		Warnings:  warnings,
	}, nil
}

func (s *QuestionService) planBlueprintSection(
	ctx context.Context,
	user *model.User,
	subjectID uint,
	state *blueprintSectionState,
	excluded map[uint]struct{},
	rng *rand.Rand,
) ([]schema.PaperBlueprintItem, map[int]int, []string, error) {
	section := state.spec
	limit := section.Count * 8
	if limit < 80 {
		limit = 80
	}
	if limit > 500 {
		limit = 500
	}
	briefs, _, err := s.List(ctx, user, subjectID, ListQuery{
		Type:              section.QuestionType,
		Status:            model.QuestionStatusPublished,
		DifficultyMin:     section.DifficultyMin,
		DifficultyMax:     section.DifficultyMax,
		KnowledgePointIDs: section.KnowledgePointIDs,
		IncludeSub:        section.IncludeSub,
		TagIDs:            section.TagIDs,
		Source:            section.Source,
		RootOnly:          true,
		Sort:              model.SortByCreatedDesc,
	}, limit, 0)
	if err != nil {
		return nil, nil, nil, err
	}

	buckets := make([][]blueprintCandidate, 6)
	for i := range briefs {
		if _, skip := excluded[briefs[i].ID]; skip {
			continue
		}
		difficulty := briefs[i].Difficulty
		if difficulty < 1 || difficulty > 5 {
			difficulty = 3
		}
		buckets[difficulty] = append(buckets[difficulty], blueprintCandidate{
			question: briefs[i],
			random:   rng.Float64(),
		})
	}
	selected := make([]schema.PaperBlueprintItem, 0, section.Count)
	target := make(map[int]int, len(section.DifficultyCounts))
	for difficulty, count := range section.DifficultyCounts {
		target[difficulty] = count
	}
	for difficulty := 1; difficulty <= 5; difficulty++ {
		for target[difficulty] > 0 {
			index := s.bestBlueprintCandidate(buckets[difficulty], state, 0)
			if index < 0 {
				break
			}
			item := s.takeBlueprintCandidate(state, buckets[difficulty][index], difficulty)
			selected = append(selected, item)
			buckets[difficulty] = append(buckets[difficulty][:index], buckets[difficulty][index+1:]...)
			target[difficulty]--
		}
	}

	for len(selected) < section.Count {
		bestDifficulty := 0
		bestIndex := -1
		bestScore := -1.0
		for difficulty := section.DifficultyMin; difficulty <= section.DifficultyMax; difficulty++ {
			index := s.bestBlueprintCandidate(buckets[difficulty], state, target[difficulty])
			if index < 0 {
				continue
			}
			score := s.blueprintScore(state, &buckets[difficulty][index])
			if target[difficulty] > 0 {
				score += 100
			}
			if score > bestScore {
				bestScore = score
				bestDifficulty = difficulty
				bestIndex = index
			}
		}
		if bestIndex < 0 {
			break
		}
		item := s.takeBlueprintCandidate(state, buckets[bestDifficulty][bestIndex], bestDifficulty)
		selected = append(selected, item)
		buckets[bestDifficulty] = append(
			buckets[bestDifficulty][:bestIndex],
			buckets[bestDifficulty][bestIndex+1:]...,
		)
	}

	actualCounts := make(map[int]int)
	for i := range selected {
		actualCounts[selected[i].QuestionBrief.Difficulty]++
	}
	warnings := make([]string, 0)
	if len(selected) < section.Count {
		warnings = append(warnings, fmt.Sprintf("「%s」只有 %d 道符合条件，少于需要的 %d 道", section.Title, len(selected), section.Count))
	}
	for difficulty, need := range section.DifficultyCounts {
		if actualCounts[difficulty] < need {
			warnings = append(warnings, fmt.Sprintf("「%s」难度 %d 还缺 %d 道", section.Title, difficulty, need-actualCounts[difficulty]))
		}
	}
	return selected, actualCounts, warnings, nil
}

func (s *QuestionService) takeBlueprintCandidate(
	state *blueprintSectionState,
	candidate blueprintCandidate,
	difficulty int,
) schema.PaperBlueprintItem {
	question := candidate.question
	for i := range question.KnowledgePoints {
		state.coverage[question.KnowledgePoints[i].ID] = struct{}{}
	}
	if question.Source != "" {
		state.sources[question.Source]++
	}
	reasons := make([]string, 0, 2)
	if difficulty > 0 {
		reasons = append(reasons, fmt.Sprintf("难度 %d", difficulty))
	}
	if len(question.KnowledgePoints) > 0 {
		reasons = append(reasons, "覆盖 "+question.KnowledgePoints[0].Name)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "符合当前分组条件")
	}
	return schema.PaperBlueprintItem{
		QuestionBrief: question,
		SectionKey:    state.spec.Key,
		SectionTitle:  state.spec.Title,
		Score:         state.spec.Score,
		Reason:        strings.Join(reasons, "，"),
	}
}

func (s *QuestionService) bestBlueprintCandidate(
	candidates []blueprintCandidate,
	state *blueprintSectionState,
	bonusDifficulty int,
) int {
	bestIndex := -1
	bestScore := math.Inf(-1)
	for i := range candidates {
		score := s.blueprintScore(state, &candidates[i])
		if bonusDifficulty > 0 {
			score += 100
		}
		if score > bestScore || (score == bestScore && (bestIndex < 0 || candidates[i].random < candidates[bestIndex].random)) {
			bestIndex = i
			bestScore = score
		}
	}
	return bestIndex
}

func (s *QuestionService) blueprintScore(state *blueprintSectionState, candidate *blueprintCandidate) float64 {
	score := 0.0
	for i := range candidate.question.KnowledgePoints {
		if _, covered := state.coverage[candidate.question.KnowledgePoints[i].ID]; !covered {
			score += 4
		}
	}
	score += float64(candidate.question.ReviewCount) * 0.75
	if candidate.question.Source != "" {
		score -= float64(state.sources[candidate.question.Source]) * 1.5
	}
	return score
}
