package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/infra/vector"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// embedBatchSize 单次向量化的文本条数。
//
// 批次太小会让 API 调用次数暴涨；太大则某些兼容端点会直接拒绝
// （DashScope 之类的上限是 20 条左右）。取 10 是在兼容性上更保险的选择。
const embedBatchSize = 10

// searchTopK 语义检索返回的候选数量。
//
// 取 8 是为了给后续的 LLM 重排留出选择余地：给模型 2~3 个候选
// 它只能在坏选项里挑，给太多又会淹没上下文。
const searchTopK = 8

// VectorService 知识点向量索引与语义检索。
type VectorService struct {
	repos    *Repos
	aiConfig *AIConfigService
}

func NewVectorService(repos *Repos, aiConfig *AIConfigService) *VectorService {
	return &VectorService{repos: repos, aiConfig: aiConfig}
}

// ---- 索引 ----

// Status 返回学科的索引状态。
func (s *VectorService) Status(ctx context.Context, user *model.User, subjectID uint) (*schema.VectorStatusResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	resp := &schema.VectorStatusResponse{SubjectID: subjectID}

	names, err := s.repos.KnowledgePoints.NamesBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp.Total = int64(len(names))

	stat, err := s.repos.Vectors.StatBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp.Indexed = stat.Indexed
	resp.Missing = resp.Total - stat.Indexed
	if resp.Missing < 0 {
		resp.Missing = 0
	}

	// 解析当前配置的向量模型。未配置不算错误——只是功能不可用。
	if resolved := s.aiConfig.ResolveOptional(ctx, model.AIModelEmbedding); resolved != nil {
		resp.ModelID = resolved.ModelID
		resp.ModelName = resolved.Display
	}

	switch {
	case resp.ModelID == 0:
		resp.Message = "尚未配置向量模型，知识点语义检索不可用。可在 AI 设置中选择一个 embedding 模型。"
	case resp.Total == 0:
		resp.Ready = true
		resp.Message = "该学科还没有知识点，无需索引。"
	case stat.Indexed == 0:
		resp.NeedsRebuild = true
		resp.Message = "索引为空，请先建立索引。"
	case stat.DistinctModels > 1 || len(stat.Dims) > 1:
		// 混用模型或维度不一致：向量空间不可比，检索结果没有意义。
		resp.NeedsRebuild = true
		resp.Message = "索引中混用了不同的向量模型或维度，检索结果不可靠，建议重建。"
	case resp.Missing > 0:
		resp.NeedsRebuild = true
		resp.Message = fmt.Sprintf("还有 %d 个知识点未索引，建议重建。", resp.Missing)
	default:
		resp.Ready = true
		resp.Message = "索引已就绪。"
	}

	return resp, nil
}

// Reindex 重建学科的知识点索引。
//
// force 为 false 时跳过内容未变的知识点——省掉重复的 API 调用；
// 为 true 时全部重算，用于换了向量模型之后。
func (s *VectorService) Reindex(ctx context.Context, user *model.User, subjectID uint, force bool) (*schema.ReindexResponse, error) {
	if err := authorize(user, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}

	resolved, err := s.aiConfig.Resolve(ctx, model.AIModelEmbedding)
	if err != nil {
		return nil, err
	}

	points, err := s.repos.KnowledgePoints.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	resp := &schema.ReindexResponse{Total: len(points), Failures: []schema.ReindexFailure{}}
	if len(points) == 0 {
		return resp, nil
	}

	started := time.Now()

	// 已索引的内容哈希，用于跳过未变化的知识点。
	existing := map[uint]model.VectorDoc{}
	if docs, err := s.repos.Vectors.ListBySubject(ctx, subjectID); err == nil {
		for i := range docs {
			existing[docs[i].KnowledgePointID] = docs[i]
		}
	}

	// 先筛出真正需要算的，再批量调用 embedding——
	// 逐条调用会把 API 调用次数放大到知识点数量级。
	type pending struct {
		point model.KnowledgePoint
		text  string
		hash  string
	}
	var todo []pending

	for i := range points {
		p := points[i]
		text := knowledgePointText(&p)
		hash := contentHash(text)

		if !force {
			if doc, ok := existing[p.ID]; ok &&
				doc.ContentHash == hash && doc.ModelID == resolved.ModelID {
				resp.Skipped++
				continue
			}
		}
		todo = append(todo, pending{point: p, text: text, hash: hash})
	}

	for start := 0; start < len(todo); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(todo) {
			end = len(todo)
		}
		batch := todo[start:end]

		texts := make([]string, 0, len(batch))
		for i := range batch {
			texts = append(texts, batch[i].text)
		}

		vectors, err := resolved.Provider.Embed(ctx, resolved.ModelName, texts)
		if err != nil {
			// 整批失败：逐条记录原因，继续处理下一批，
			// 避免一个坏知识点让整个重建中断。
			for i := range batch {
				resp.Failed++
				resp.Failures = appendFailure(resp.Failures, batch[i].point, err)
			}
			continue
		}
		if len(vectors) != len(batch) {
			for i := range batch {
				resp.Failed++
				resp.Failures = appendFailure(resp.Failures, batch[i].point,
					fmt.Errorf("返回向量数量不符：期望 %d，实际 %d", len(batch), len(vectors)))
			}
			continue
		}

		for i := range batch {
			doc := &model.VectorDoc{
				KnowledgePointID: batch[i].point.ID,
				SubjectID:        subjectID,
				ModelID:          resolved.ModelID,
				ContentHash:      batch[i].hash,
				Dim:              len(vectors[i]),
				Embedding:        vector.Encode(vectors[i]),
			}
			if err := s.repos.Vectors.Upsert(ctx, doc); err != nil {
				resp.Failed++
				resp.Failures = appendFailure(resp.Failures, batch[i].point, err)
				continue
			}
			resp.Indexed++
		}
	}

	resp.ElapsedMs = time.Since(started).Milliseconds()
	return resp, nil
}

// IndexKnowledgePoint 重建单个知识点的索引。
//
// 知识点增删改时调用。失败不返回错误——索引是"尽力而为"的辅助能力，
// 不该因为它挂掉就让用户的编辑操作失败；后续重建索引即可补上。
func (s *VectorService) IndexKnowledgePoint(ctx context.Context, subjectID, kpID uint) {
	resolved := s.aiConfig.ResolveOptional(ctx, model.AIModelEmbedding)
	if resolved == nil {
		return
	}

	kp, err := s.repos.KnowledgePoints.Get(ctx, kpID)
	if err != nil {
		return
	}

	text := knowledgePointText(kp)
	vectors, err := resolved.Provider.Embed(ctx, resolved.ModelName, []string{text})
	if err != nil || len(vectors) != 1 {
		return
	}

	_ = s.repos.Vectors.Upsert(ctx, &model.VectorDoc{
		KnowledgePointID: kpID,
		SubjectID:        subjectID,
		ModelID:          resolved.ModelID,
		ContentHash:      contentHash(text),
		Dim:              len(vectors[0]),
		Embedding:        vector.Encode(vectors[0]),
	})
}

// DeleteKnowledgePointVectors 删除知识点的向量。
func (s *VectorService) DeleteKnowledgePointVectors(ctx context.Context, kpIDs []uint) {
	_ = s.repos.Vectors.DeleteByKPs(ctx, kpIDs)
}

// ---- 检索 ----

// Search 在学科范围内做语义检索。
func (s *VectorService) Search(ctx context.Context, user *model.User, subjectID uint, query string, topK int) (*schema.VectorSearchResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, apperr.New(apperr.CodeInvalidParam, "查询内容不能为空")
	}
	if topK <= 0 || topK > 50 {
		topK = searchTopK
	}

	resolved, err := s.aiConfig.Resolve(ctx, model.AIModelEmbedding)
	if err != nil {
		return nil, err
	}

	vectors, err := resolved.Provider.Embed(ctx, resolved.ModelName, []string{query})
	if err != nil {
		return nil, apperr.New(apperr.CodeAIUnavailable,
			"查询向量化失败："+aiagentHumanize(err)).WithCause(err)
	}
	if len(vectors) != 1 {
		return nil, apperr.New(apperr.CodeAIUnavailable, "向量模型未返回查询向量")
	}

	hits, err := s.searchByVector(ctx, subjectID, vectors[0], topK)
	if err != nil {
		return nil, err
	}

	resp := &schema.VectorSearchResponse{
		Query:     query,
		Results:   make([]schema.VectorSearchItem, 0, len(hits)),
		ModelName: resolved.Display,
	}
	for _, h := range hits {
		resp.Results = append(resp.Results, schema.VectorSearchItem{
			KnowledgePointID: h.ID,
			Name:             metaString(h.Meta, "name"),
			Path:             metaString(h.Meta, "path"),
			Score:            h.Score,
		})
	}
	return resp, nil
}

// searchByVector 在进程内对学科向量做相似度检索。
func (s *VectorService) searchByVector(ctx context.Context, subjectID uint, query []float32, topK int) ([]vector.Scored, error) {
	docs, err := s.repos.Vectors.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if len(docs) == 0 {
		return nil, nil
	}

	// 需要知识点名称与路径作为返回值，一次性取出来避免逐条查库。
	points, err := s.repos.KnowledgePoints.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	meta := make(map[uint]model.KnowledgePoint, len(points))
	for i := range points {
		meta[points[i].ID] = points[i]
	}

	items := make([]vector.Item, 0, len(docs))
	for i := range docs {
		vec, err := vector.Decode(docs[i].Embedding)
		if err != nil {
			// 单条损坏跳过即可，不影响其余检索。
			continue
		}
		if !vector.SameDim(vec, query) {
			// 维度不一致说明索引里混了其它模型的向量，跳过而不是算出无意义的分数。
			continue
		}
		kp := meta[docs[i].KnowledgePointID]
		items = append(items, vector.Item{
			ID:     docs[i].KnowledgePointID,
			Vector: vec,
			Meta: map[string]any{
				"name": kp.Name,
				"path": kp.Path,
			},
		})
	}

	// 阈值 0.25：留得比较宽松，因为向量检索只负责"从上千个知识点里
	// 粗筛出几个候选"，精挑由后续的 LLM 重排负责。
	// 这里卡太严会把正确答案筛掉，而漏掉的候选是没法补救的。
	return vector.Search(query, items, topK, 0.25), nil
}

// ---- 为导入做知识点匹配 ----

// SuggestKnowledgePoints 为一段文本推荐知识点。
//
// 两段式：向量检索粗筛候选 → LLM 在候选里精挑。直接让 LLM 从上千个
// 知识点里选既不现实（超出上下文）也不可靠；而只靠向量检索，
// 同义不同词的表述又会漏掉。先用向量把范围缩到 8 个，再让模型判断，
// 是成本与准确率之间比较实际的取舍。
func (s *VectorService) SuggestKnowledgePoints(ctx context.Context, subjectID uint, text string, limit int) []schema.KnowledgeSuggestion {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 || limit > 10 {
		limit = 3
	}

	resolved := s.aiConfig.ResolveOptional(ctx, model.AIModelEmbedding)
	if resolved == nil {
		return nil
	}

	vectors, err := resolved.Provider.Embed(ctx, resolved.ModelName, []string{truncateRunes(text, 500)})
	if err != nil || len(vectors) != 1 {
		return nil
	}

	hits, err := s.searchByVector(ctx, subjectID, vectors[0], searchTopK)
	if err != nil || len(hits) == 0 {
		return nil
	}

	out := make([]schema.KnowledgeSuggestion, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		out = append(out, schema.KnowledgeSuggestion{
			KnowledgePointID: h.ID,
			Name:             metaString(h.Meta, "name"),
			Path:             metaString(h.Meta, "path"),
			Score:            h.Score,
			Source:           "vector",
		})
	}
	return out
}

// ---- 辅助 ----

// knowledgePointText 组装用于向量化的文本。
//
// 拼上父级路径：单看"单调性"三个字缺乏上下文，而"函数 / 单调性"
// 能让向量更靠近"判断函数单调性"这类题干。
func knowledgePointText(kp *model.KnowledgePoint) string {
	var sb strings.Builder
	sb.WriteString(kp.Name)
	if kp.Description != "" {
		sb.WriteString("。")
		sb.WriteString(kp.Description)
	}
	if kp.Path != "" {
		sb.WriteString("（层级：")
		sb.WriteString(strings.ReplaceAll(strings.Trim(kp.Path, "/"), "/", " / "))
		sb.WriteString("）")
	}
	return sb.String()
}

func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta[key].(string); ok {
		return v
	}
	return ""
}

func appendFailure(list []schema.ReindexFailure, kp model.KnowledgePoint, err error) []schema.ReindexFailure {
	// 只保留前 20 条：失败几百条时全量返回没有意义，
	// 看前几条就知道是共性问题还是个别数据问题。
	if len(list) >= 20 {
		return list
	}
	return append(list, schema.ReindexFailure{
		KnowledgePointID: kp.ID,
		Name:             kp.Name,
		Error:            aiagentHumanize(err),
	})
}

// aiagentHumanize 复用 aiagent 的错误翻译，保证对外文案一致。
func aiagentHumanize(err error) string {
	if err == nil {
		return ""
	}
	return humanizeAIError(err)
}
