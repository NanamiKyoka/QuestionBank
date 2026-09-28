package importing

import (
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// Mode 导入模式。
type Mode string

const (
	// ModeExtract 抽取新题：从文档里识别出题目。
	ModeExtract Mode = "extract"
	// ModeSolve 补写答案：题目已有，让模型给出答案与解析。
	ModeSolve Mode = "solve"
)

func (m Mode) Valid() bool { return m == ModeExtract || m == ModeSolve }

// extractSchema 给模型的输出结构说明。
//
// 刻意用注释式的文字说明而不是严格的 JSON Schema：多数 OpenAI 兼容端点
// 只支持 json_object 模式，不接受完整的 json_schema 约束。
// 把结构写进提示词里是兼容性最好的做法。
const extractSchema = `{
  "questions": [
    {
      "temp_id": "题目的临时编号，字符串，用于子题关联母题",
      "parent_temp_id": "若本题是某道母题的小问，填母题的 temp_id，否则省略",
      "question_type": "single_choice | multiple_choice | fill_blank | true_false | essay",
      "stem": "题干内容，Markdown 格式，行内公式写成 $...$，独立公式写成 $$...$$，填空题的空位写成六个下划线 ______",
      "options": [ {"label": "A", "content": "选项内容"} ],
      "answer": "参考答案。选择题只写选项字母，如 B 或 BD；判断题写 对 或 错；填空题多个空用分号分隔，同一个空的多个可接受答案用 或 分隔；解答题写完整答案",
      "analysis": "解析过程，Markdown 格式，可选",
      "difficulty": 1,
      "knowledge_points": ["涉及的知识点名称"],
      "tags": ["建议的标签"]
    }
  ]
}`

// solveSchema 补写答案模式的结构说明。
const solveSchema = `{
  "questions": [
    {
      "temp_id": "与输入题目对应的编号",
      "stem": "原样返回题干，不要改动",
      "answer": "你给出的答案",
      "analysis": "解析过程"
    }
  ]
}`

// PromptOptions 生成提示词的参数。
type PromptOptions struct {
	Mode Mode
	// SubjectName 学科名，帮助模型理解上下文。
	SubjectName string
	// ExistingKnowledgePoints 已有知识点，供模型优先复用而不是新造。
	ExistingKnowledgePoints []string
	// ExistingTags 已有标签。
	ExistingTags []string
}

// BuildSystemPrompt 生成系统提示词。
func BuildSystemPrompt(opt PromptOptions) string {
	var sb strings.Builder

	sb.WriteString("你是一位资深的中小学与大学学科教研员，擅长把试卷、讲义、教案等材料整理成结构化的题库数据。\n\n")

	switch opt.Mode {
	case ModeSolve:
		sb.WriteString("任务：为下面给出的每一道题补写答案与解析。题干必须原样返回，不要改写、不要增删题号。\n")
	default:
		sb.WriteString("任务：从下面的材料中识别出所有题目，并整理成结构化数据。\n")
		sb.WriteString("注意：\n")
		sb.WriteString("1. 材料里可能有标题、考号说明、分数说明等非题目内容，这些不要当成题目。\n")
		sb.WriteString("2. 每道题必须包含题干；如果材料里有选项、答案、解析，一并提取。\n")
		sb.WriteString("3. 如果某道题没有给出答案，answer 留空字符串，不要自己编造。\n")
		sb.WriteString("4. 材料里出现的小题（如「(1)」「(2)」）作为母题的小问处理，用 parent_temp_id 关联。\n")
	}

	sb.WriteString("\n数学公式必须使用 LaTeX：行内用 $...$，独立成行用 $$...$$。\n")
	sb.WriteString("例如：函数 $f(x) = \\frac{1}{2}x^2$ 在 $x=1$ 处的导数为 $f'(1)$。\n")
	sb.WriteString("不要使用 Unicode 数学符号（如 ×÷≤），一律用 LaTeX 命令（\\times \\div \\leq）。\n")

	sb.WriteString("\n填空题的空位统一写成六个下划线：______ 。答案里同一个空的多个可接受写法用「或」分隔。\n")

	if opt.SubjectName != "" {
		sb.WriteString("\n本材料属于学科：" + opt.SubjectName + "\n")
	}
	if len(opt.ExistingKnowledgePoints) > 0 {
		sb.WriteString("\n知识点请优先从下列已有清单中选取（原样使用名称，不要改写）：\n")
		sb.WriteString("  " + strings.Join(limitStrings(opt.ExistingKnowledgePoints, 200), "、") + "\n")
		sb.WriteString("清单里确实没有合适的，才新建知识点名称。\n")
	}
	if len(opt.ExistingTags) > 0 {
		sb.WriteString("\n标签请优先从下列已有清单中选取：\n")
		sb.WriteString("  " + strings.Join(limitStrings(opt.ExistingTags, 100), "、") + "\n")
	}

	schema := extractSchema
	if opt.Mode == ModeSolve {
		schema = solveSchema
	}

	sb.WriteString("\n严格只输出一个 JSON 对象，不要输出任何解释文字，也不要用 Markdown 代码块包裹。\n")
	sb.WriteString("输出结构如下（这是结构说明，不是示例数据）：\n")
	sb.WriteString(schema)

	return sb.String()
}

// limitStrings 限制拼接进提示词的条目数量。
//
// 知识点可能有几百个，全塞进提示词会挤占上下文、增加成本，
// 而且模型面对超长清单时反而更容易乱选。截断是必要的取舍。
func limitStrings(in []string, max int) []string {
	if len(in) <= max {
		return in
	}
	return in[:max]
}

// BuildUserContent 组装用户消息：在原文前加上必要的定位信息。
func BuildUserContent(content string, opt PromptOptions) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if opt.Mode == ModeSolve {
		return "请为下列题目补写答案与解析：\n\n" + content
	}
	return "请从下列材料中提取题目：\n\n" + content
}

// DefaultExtractPromptKey 系统设置里可覆盖的提示词键。
const DefaultExtractPromptKey = model.SettingAIExtractPrompt
