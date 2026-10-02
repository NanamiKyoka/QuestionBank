package schema

import "github.com/NanamiKyoka/questionmind/internal/model"

// SolveRequest 让模型为题库里已有的题目补写答案与解析。
//
// 刻意以**单题**为粒度：批量交给前端循环调用。
//   - 一道题的求解要几十秒，二十道题塞进一个请求必然撞上任何客户端的超时；
//   - 逐题返回意味着每做完一道就已经落库，中途关页只丢没做到的那几道；
//   - 进度条能做在题上，而不是一个转了三分钟还不知道到哪儿的圈。
type SolveRequest struct {
	// Overwrite 题目已有答案时是否覆盖。
	//
	// 默认 false：AI 解题最常见的用途是给「仅录题」进来的题目补答案，
	// 而那些题本来是空的。把一份已经人工核对过的答案无声换成模型输出，
	// 是这套系统里最不该发生的事之一。
	Overwrite bool `json:"overwrite"`
}

// SolveResult 单题求解的结果。
type SolveResult struct {
	QuestionID uint `json:"question_id"`
	// Status: solved（已写入） | skipped（没动，原因见 message） | failed（模型或解析出错）。
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`

	// Answer 写入后的答案结构，便于前端直接把这一行刷新成新状态。
	Answer model.AnswerSpec `json:"answer"`
	// Analysis 写入后的解析（Markdown 转成的富文本）。
	Analysis model.JSON `json:"analysis,omitempty"`
	// AnalysisMarkdown 模型给的解析原文，用于在界面上原样展示（富文本不便预览）。
	AnalysisMarkdown string `json:"analysis_markdown,omitempty"`
	// AnsweredText 人可读的答案摘要，例如 "B"、"x=1；y=2"、"参考解析 320 字"。
	AnsweredText string `json:"answered_text,omitempty"`
	// Warnings 解析过程中的问题（如"识别到多个正确答案"）。
	Warnings []string `json:"warnings,omitempty"`

	// Question 更新后的题目详情，前端据此刷新列表行与详情抽屉。
	Question *QuestionDetail `json:"question,omitempty"`
	// Model 本次实际使用的模型名，便于回答"这条答案是谁写的"。
	Model string `json:"model,omitempty"`
}
