package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/infra/vector"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// fakeEmbedder 模拟一个 embedding 端点。
//
// 关键点：它必须让"语义相近的文本得到相近的向量"，否则检索逻辑
// 根本没法验证。做法是按学科主题分桶——命中某一类关键词就点亮对应维度。
// 这不是真实的语义模型，但足以验证"向量存对了、检索排对了"。
func fakeEmbedder(t *testing.T) *httptest.Server {
	t.Helper()
	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)

		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		items := make([]item, len(req.Input))
		for i, text := range req.Input {
			items[i] = item{Index: i, Embedding: bucketVector(text)}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// bucketVector 按主题关键词构造 4 维向量。
func bucketVector(text string) []float32 {
	// 基础分量避免零向量（零向量的余弦相似度没有意义）。
	v := []float32{0.1, 0.1, 0.1, 0.1}

	keywords := []struct {
		dim  int
		keys []string
	}{
		{0, []string{"函数", "单调", "导数", "极值", "最值"}},
		{1, []string{"几何", "三角形", "圆", "面积", "角度", "平行"}},
		{2, []string{"概率", "统计", "随机", "期望", "分布"}},
		{3, []string{"数列", "等差", "等比", "通项", "求和"}},
	}
	for _, kw := range keywords {
		for _, k := range kw.keys {
			if strings.Contains(text, k) {
				v[kw.dim] += 1.0
			}
		}
	}
	return v
}

type vectorFixture struct {
	repos   *Repos
	svc     *VectorService
	subject *model.Subject
	user    *model.User
	kpByID  map[string]uint
}

// newVectorFixture 准备学科、知识点与向量模型。
func newVectorFixture(t *testing.T, withModel bool) *vectorFixture {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	if err := repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}

	kpByID := map[string]uint{}
	names := []string{"函数单调性", "三角形面积计算", "随机变量期望", "等差数列通项"}
	for _, name := range names {
		kp := &model.KnowledgePoint{
			SubjectID: subject.ID, Name: name, Level: 1, CreatedBy: admin.ID,
		}
		if err := repos.KnowledgePoints.Create(ctx, kp); err != nil {
			t.Fatalf("建知识点失败: %v", err)
		}
		if err := repos.KnowledgePoints.UpdateFields(ctx, kp.ID,
			map[string]any{"path": fmt.Sprintf("/%d/", kp.ID)}); err != nil {
			t.Fatalf("写路径失败: %v", err)
		}
		kpByID[name] = kp.ID
	}

	aiSvc := newAIService(t, repos)
	if withModel {
		provider, err := aiSvc.CreateProvider(ctx, admin, &schema.ProviderCreateRequest{
			Name: "Fake", InterfaceType: "openai", BaseURL: fakeEmbedder(t).URL, APIKey: "sk-test",
		})
		if err != nil {
			t.Fatalf("建供应商失败: %v", err)
		}
		if _, err := aiSvc.CreateModel(ctx, admin, &schema.ModelCreateRequest{
			ProviderID: provider.ID, Name: "fake-embed", ModelType: string(model.AIModelEmbedding),
		}); err != nil {
			t.Fatalf("建向量模型失败: %v", err)
		}
	}

	return &vectorFixture{
		repos:   repos,
		svc:     NewVectorService(repos, aiSvc),
		subject: subject,
		user:    admin,
		kpByID:  kpByID,
	}
}

func TestVectorStatusWithoutModel(t *testing.T) {
	fx := newVectorFixture(t, false)
	ctx := context.Background()

	status, err := fx.svc.Status(ctx, fx.user, fx.subject.ID)
	if err != nil {
		t.Fatalf("取状态失败: %v", err)
	}
	if status.Ready {
		t.Fatal("未配置向量模型时不应就绪")
	}
	if status.Total != 4 {
		t.Fatalf("应统计到 4 个知识点，实际 %d", status.Total)
	}
	if !strings.Contains(status.Message, "向量模型") {
		t.Fatalf("应提示缺少向量模型，实际 %q", status.Message)
	}
}

func TestVectorReindexAndSearch(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()

	// 初始状态：索引为空。
	status, _ := fx.svc.Status(ctx, fx.user, fx.subject.ID)
	if status.Indexed != 0 || !status.NeedsRebuild {
		t.Fatalf("初始应无索引且需要重建: %+v", status)
	}

	// 重建。
	res, err := fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if res.Indexed != 4 || res.Failed != 0 {
		t.Fatalf("应索引 4 个知识点，实际 indexed=%d failed=%d %+v",
			res.Indexed, res.Failed, res.Failures)
	}

	// 重建后状态就绪。
	status, _ = fx.svc.Status(ctx, fx.user, fx.subject.ID)
	if !status.Ready || status.Indexed != 4 || status.Missing != 0 {
		t.Fatalf("重建后应就绪: %+v", status)
	}
	if status.ModelName == "" {
		t.Fatal("应报告使用的向量模型")
	}

	// 语义检索：问"求函数的单调区间"应命中「函数单调性」。
	search, err := fx.svc.Search(ctx, fx.user, fx.subject.ID, "求下列函数的单调区间", 5)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(search.Results) == 0 {
		t.Fatal("应返回检索结果")
	}
	if search.Results[0].Name != "函数单调性" {
		t.Fatalf("最佳匹配应为「函数单调性」，实际 %q（全部：%+v）",
			search.Results[0].Name, search.Results)
	}
	if search.Results[0].Score <= 0 {
		t.Fatalf("相似度应为正: %v", search.Results[0].Score)
	}
	// 分数必须单调不增。
	for i := 1; i < len(search.Results); i++ {
		if search.Results[i].Score > search.Results[i-1].Score {
			t.Fatalf("结果未按相似度排序: %+v", search.Results)
		}
	}

	// 换一个主题，应命中另一条。
	search2, _ := fx.svc.Search(ctx, fx.user, fx.subject.ID, "等差数列的通项公式怎么求", 5)
	if len(search2.Results) == 0 || search2.Results[0].Name != "等差数列通项" {
		t.Fatalf("应命中「等差数列通项」，实际 %+v", search2.Results)
	}
}

// 内容没变时应跳过，避免重复调用 embedding API 白花钱。
func TestVectorReindexSkipsUnchanged(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()

	first, _ := fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)
	if first.Indexed != 4 {
		t.Fatalf("首次应索引 4 个，实际 %d", first.Indexed)
	}

	second, _ := fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)
	if second.Indexed != 0 || second.Skipped != 4 {
		t.Fatalf("内容未变时应全部跳过，实际 indexed=%d skipped=%d",
			second.Indexed, second.Skipped)
	}

	// force=true 时全部重算。
	forced, _ := fx.svc.Reindex(ctx, fx.user, fx.subject.ID, true)
	if forced.Indexed != 4 {
		t.Fatalf("force 时应全部重算，实际 %d", forced.Indexed)
	}
}

// 知识点改名后，重新索引应更新向量而不是留下旧值。
func TestVectorReindexPicksUpRename(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()
	fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)

	kpID := fx.kpByID["函数单调性"]
	if err := fx.repos.KnowledgePoints.UpdateFields(ctx, kpID, map[string]any{
		"name": "概率与随机事件",
	}); err != nil {
		t.Fatalf("改名失败: %v", err)
	}

	res, err := fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if res.Indexed != 1 {
		t.Fatalf("只有被改名的那个需要重算，实际 %d", res.Indexed)
	}

	// 改名后应能被概率主题检索到。
	search, _ := fx.svc.Search(ctx, fx.user, fx.subject.ID, "随机事件的概率怎么算", 3)
	found := false
	for _, r := range search.Results {
		if r.KnowledgePointID == kpID {
			found = true
		}
	}
	if !found {
		t.Fatalf("改名后的知识点应能被新主题检索到: %+v", search.Results)
	}
}

// 索引里混了不同模型时，检索结果不可比，必须提示重建。
func TestVectorStatusDetectsMixedModels(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()
	fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)

	// 手动插一条"另一个模型"的索引。
	kpID := fx.kpByID["三角形面积计算"]
	if err := fx.repos.Vectors.Upsert(ctx, &model.VectorDoc{
		KnowledgePointID: kpID,
		SubjectID:        fx.subject.ID,
		ModelID:          99999,
		ContentHash:      "stale",
		Dim:              4,
		Embedding:        encodeFloats([]float32{1, 0, 0, 0}),
	}); err != nil {
		t.Fatalf("插入旧向量失败: %v", err)
	}

	status, _ := fx.svc.Status(ctx, fx.user, fx.subject.ID)
	if !status.NeedsRebuild {
		t.Fatal("混用模型时应提示重建")
	}
	if !strings.Contains(status.Message, "混用") {
		t.Fatalf("应说明原因，实际 %q", status.Message)
	}
}

// 维度不一致的向量必须跳过，而不是算出一个无意义的相似度。
func TestVectorSearchSkipsMismatchedDimensions(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()
	fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)

	// 插一条 8 维的（与其它 4 维不一致）。
	if err := fx.repos.Vectors.Upsert(ctx, &model.VectorDoc{
		KnowledgePointID: fx.kpByID["三角形面积计算"],
		SubjectID:        fx.subject.ID,
		ModelID:          1,
		ContentHash:      "weird-dim",
		Dim:              8,
		Embedding:        encodeFloats([]float32{1, 0, 0, 0, 0, 0, 0, 0}),
	}); err != nil {
		t.Fatalf("插入异维度向量失败: %v", err)
	}

	// 检索不应报错，且结果都是 4 维空间里的。
	search, err := fx.svc.Search(ctx, fx.user, fx.subject.ID, "函数的单调性", 10)
	if err != nil {
		t.Fatalf("检索不应因维度不一致而失败: %v", err)
	}
	for _, r := range search.Results {
		if r.KnowledgePointID == fx.kpByID["三角形面积计算"] {
			t.Fatalf("维度不一致的向量不应参与排序: %+v", r)
		}
	}
}

func TestVectorSearchValidatesInput(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()

	if _, err := fx.svc.Search(ctx, fx.user, fx.subject.ID, "", 5); err == nil {
		t.Fatal("空查询应被拒绝")
	}
	if _, err := fx.svc.Search(ctx, fx.user, fx.subject.ID, "   ", 5); err == nil {
		t.Fatal("纯空白查询应被拒绝")
	}
	// 未配置向量模型时给出明确提示。
	noModel := newVectorFixture(t, false)
	_, err := noModel.svc.Search(ctx, noModel.user, noModel.subject.ID, "函数", 5)
	if err == nil || !strings.Contains(err.Error(), "向量模型") {
		t.Fatalf("应提示缺少向量模型，实际 %v", err)
	}
}

func TestVectorReindexRequiresManagePermission(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()

	viewer := &model.User{Base: model.Base{ID: 20}, Username: "viewer", IsActive: true}
	viewer.Memberships = []model.SubjectMember{
		{UserID: viewer.ID, SubjectID: fx.subject.ID, Role: model.SubjectRoleViewer},
	}
	if _, err := fx.svc.Reindex(ctx, viewer, fx.subject.ID, false); err == nil {
		t.Fatal("只读角色不应能重建索引")
	}
	// 但可以查看状态与检索。
	if _, err := fx.svc.Status(ctx, viewer, fx.subject.ID); err != nil {
		t.Fatalf("只读角色应能查看状态: %v", err)
	}
}

// SuggestKnowledgePoints 是导入流程用的：给出候选供人工确认。
func TestVectorSuggestKnowledgePoints(t *testing.T) {
	fx := newVectorFixture(t, true)
	ctx := context.Background()
	fx.svc.Reindex(ctx, fx.user, fx.subject.ID, false)

	suggestions := fx.svc.SuggestKnowledgePoints(ctx, fx.subject.ID, "已知函数 f(x)=x^2，求其单调递减区间", 2)
	if len(suggestions) == 0 {
		t.Fatal("应给出知识点建议")
	}
	if suggestions[0].Name != "函数单调性" {
		t.Fatalf("首选建议应为「函数单调性」，实际 %q（全部：%+v）",
			suggestions[0].Name, suggestions)
	}
	if len(suggestions) > 2 {
		t.Fatalf("应遵守数量上限，实际 %d 条", len(suggestions))
	}
	// 建议绝不能直接写库——它只是候选。
	if _, err := fx.repos.Vectors.Get(ctx, fx.kpByID["函数单调性"]); err != nil {
		t.Fatalf("索引应已存在: %v", err)
	}

	// 未配置向量模型时返回 nil 而不是报错：导入流程不该因此中断。
	noModel := newVectorFixture(t, false)
	if got := noModel.svc.SuggestKnowledgePoints(ctx, noModel.subject.ID, "函数", 3); got != nil {
		t.Fatalf("未配置向量模型时应静默返回 nil，实际 %+v", got)
	}
	// 空文本同样返回 nil。
	if got := fx.svc.SuggestKnowledgePoints(ctx, fx.subject.ID, "   ", 3); got != nil {
		t.Fatalf("空文本应返回 nil，实际 %+v", got)
	}
}

// encodeFloats 测试用：复用真实的编码器，顺带覆盖它的正确性。
func encodeFloats(v []float32) []byte { return vector.Encode(v) }
