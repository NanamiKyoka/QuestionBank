package importing

import (
	"strings"
	"testing"
)

// 补写答案的输出契约必须写清 answer 的格式。
//
// extractChoiceLetters 只认「整串都是选项字母」的答案，而这条契约此前
// 只写了「你给出的答案」。模型于是按自己的习惯作答——
// "正确答案是选项 B（$x=3$）"、"故选 B"、甚至直接复述选项内容——
// 这些全被解析器判成"未能识别答案，请手动选择正确选项"。
// 用户看到的现象是"AI 解题没看选项"，实际是契约漏了一句格式约定。
func TestSolveSchemaStatesChoiceAnswerFormat(t *testing.T) {
	for _, want := range []string{
		"单选题只写一个选项字母",
		"多选题只写选项字母",
		"判断题只写",
		"不要写「答案是」",
	} {
		if !strings.Contains(solveSchema, want) {
			t.Fatalf("solveSchema 缺少答案格式约定 %q", want)
		}
	}
}

// 抽取与补写两条路径共用同一份格式约定：一旦只改一边，另一边就会开始
// 返回"未能识别答案"，而两边用的是同一个解析器。
func TestExtractAndSolveSchemasAgreeOnAnswerFormat(t *testing.T) {
	const phrase = "单选题只写"
	if !strings.Contains(extractSchema, phrase) {
		t.Fatalf("extractSchema 缺少 %q", phrase)
	}
	if !strings.Contains(solveSchema, phrase) {
		t.Fatalf("solveSchema 缺少 %q", phrase)
	}
}

// 补写模式的系统提示词要说明"选项是题目的一部分"。
// 不说，模型很容易只盯着题干作答，把选项当成无关的附注。
func TestDefaultSolveTemplateMentionsOptions(t *testing.T) {
	if !strings.Contains(defaultSolveTemplate, "选项") {
		t.Fatal("补写答案的默认提示词没有提到选项")
	}
	if !strings.Contains(defaultSolveTemplate, "不要自行判断或更改题型") {
		t.Fatal("补写答案的默认提示词应说明题型已给定")
	}
}
