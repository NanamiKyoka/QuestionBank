package model

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// 前端题库页固定发送 "updated_at_desc"（QuestionsView.vue 的默认排序）。
// 它不在早期白名单里，于是静默退化成 created_desc——
// 界面上「更新于」这一列看起来能用，实际排序的却是创建时间。
func TestQuestionSortAcceptsUpdatedAtDesc(t *testing.T) {
	got := SortByUpdatedAtDesc.Normalize()
	if got != SortByUpdatedAtDesc {
		t.Fatalf("updated_at_desc 不应被回落，实际得到 %q", got)
	}
	if !SortByUpdatedAtDesc.Valid() {
		t.Fatal("updated_at_desc 应被标记为合法排序")
	}
	if clause := SortByUpdatedAtDesc.OrderClause(); clause != "questions.updated_at DESC" {
		t.Fatalf("排序子句不对，实际 %q", clause)
	}
	// 与既有别名必须完全等价，否则同一份数据会出现两种排序结果。
	if SortByUpdatedAtDesc.OrderClause() != SortByUpdatedDesc.OrderClause() {
		t.Fatal("updated_at_desc 与 updated_desc 应产生相同的排序子句")
	}
}

// 白名单里的每一个取值都必须既合法、又真的有对应的 ORDER BY 子句，
// 否则就会出现"接受了却按别的排"的静默错误。
func TestQuestionSortWhitelistIsConsistent(t *testing.T) {
	cases := []struct {
		sort   QuestionSort
		clause string
	}{
		{SortByCreatedDesc, "questions.created_at DESC"},
		{SortByCreatedAsc, "questions.created_at ASC"},
		{SortByUpdatedDesc, "questions.updated_at DESC"},
		{SortByUpdatedAtDesc, "questions.updated_at DESC"},
		{SortByDifficultyAsc, "questions.difficulty ASC, questions.id DESC"},
		{SortByDifficultyDesc, "questions.difficulty DESC, questions.id DESC"},
	}
	for _, tc := range cases {
		if !tc.sort.Valid() {
			t.Fatalf("%q 应在白名单内", tc.sort)
		}
		if got := tc.sort.Normalize(); got != tc.sort {
			t.Fatalf("%q 归一化后变成了 %q", tc.sort, got)
		}
		if got := tc.sort.OrderClause(); got != tc.clause {
			t.Fatalf("%q 的排序子句 = %q，期望 %q", tc.sort, got, tc.clause)
		}
	}
}

func TestQuestionSortNormalizeFallsBack(t *testing.T) {
	for _, in := range []QuestionSort{"", "nonsense", "updated_at", "created_at_desc", "DESC"} {
		if got := in.Normalize(); got != SortByCreatedDesc {
			t.Fatalf("%q 应回落到 %q，实际 %q", in, SortByCreatedDesc, got)
		}
	}
}

// 回落必须留下痕迹：否则用户只会看到"排序不对"，排查时没有任何线索。
func TestQuestionSortFallbackIsLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	SortByUpdatedAtDesc.Normalize()
	if buf.Len() != 0 {
		t.Fatalf("合法排序不应产生回落日志，实际输出：%s", buf.String())
	}

	if got := QuestionSort("created_at_desc").Normalize(); got != SortByCreatedDesc {
		t.Fatalf("非法值应回落到 %q，实际 %q", SortByCreatedDesc, got)
	}
	out := buf.String()
	if !strings.Contains(out, "排序参数不合法") {
		t.Fatalf("非法排序应打出回落日志，实际输出：%s", out)
	}
	// 日志必须带上用户实际传的值，否则还得回头翻代码才知道是哪端传错了。
	if !strings.Contains(out, "created_at_desc") {
		t.Fatalf("回落日志应带上原始取值，实际输出：%s", out)
	}

	// 没传排序参数是正常路径，不该刷日志。
	buf.Reset()
	if got := QuestionSort("").Normalize(); got != SortByCreatedDesc {
		t.Fatalf("空值应回落到 %q，实际 %q", SortByCreatedDesc, got)
	}
	if buf.Len() != 0 {
		t.Fatalf("未传排序参数不应产生日志，实际输出：%s", buf.String())
	}
}
