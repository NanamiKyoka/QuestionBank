package importing

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// Mode 导入模式。
type Mode string

const (
	// ModeExtract 抽取新题：从文档里识别出题目，材料里有答案就连答案一起抽。
	ModeExtract Mode = "extract"
	// ModeSolve 补写答案：题目已有，让模型给出答案与解析。
	ModeSolve Mode = "solve"
	// ModeRecord 仅录题：只识别题目本身，不要答案与解析。
	//
	// 它存在的理由是成本与等待：完整抽取要为每道解答题写出推导
	// （实测约 1370 输出 token/题），一份二十题的卷子近三万 token，
	// 几分钟起步且极易撞上供应商的超时上限。答案与解析改由老师填写，
	// 或者之后在题库里单独让模型补写。
	ModeRecord Mode = "record"
)

func (m Mode) Valid() bool {
	return m == ModeExtract || m == ModeSolve || m == ModeRecord
}

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
      "figure_box": "可选，[x,y,w,h]，归一化到 0~1，表示该题在当前页面中的图形区域；没有题图则省略",
      "answer": "参考答案。严格按题型给出：单选题只写一个选项字母（如 B）；多选题只写选项字母（如 BD 或 B、D）；判断题只写「对」或「错」；填空题多个空用分号分隔，同一个空的多个可接受答案用 或 分隔；解答题写完整答案。不要写「答案是」「故选」等前后缀，也不要在答案里附加解释文字",
      "analysis": "解析过程，Markdown 格式，可选",
      "difficulty": 1,
      "knowledge_points": ["涉及的知识点名称"],
      "tags": ["建议的标签"]
    }
  ]
}`

// solveSchema 补写答案模式的结构说明。
//
// answer 字段的格式约定是这里最要紧的一行。曾经只写「你给出的答案」，
// 模型于是按自己的习惯作答："正确答案是选项 B（$x=3$）"、"故选 B"、
// 甚至直接复述选项内容。而解析侧 extractChoiceLetters 要求整串都是
// 选项字母才肯认，于是这些作答全被判成"未能识别答案，请手动选择正确选项"——
// 用户看到的现象是"AI 解题没看选项 / 没按题目作答"，实际是契约没写清楚。
// 抽取模式的 extractSchema 一直带着同样的格式说明，补写模式漏了。
const solveSchema = `{
  "questions": [
    {
      "temp_id": "与输入题目对应的编号",
      "stem": "原样返回题干，不要改动",
      "answer": "答案，严格按题型给出：单选题只写一个选项字母（如 B）；多选题只写选项字母（如 BD 或 B、D）；判断题只写「对」或「错」；填空题各空用分号分隔、同一空的多个可接受写法用「或」分隔；解答题写完整答案。不要写「答案是」「故选」等前后缀，也不要在答案里附加解释文字",
      "analysis": "解析过程，Markdown 格式"
    }
  ]
}`

// recordSchema 仅录题模式的结构说明。
//
// 与 extractSchema 的差别只有两处，而且都是**减法**：不要求 analysis，
// 并把 answer 固定成空串。求解正是输出里最贵的那部分（解析动辄上千 token/题），
// 把这两个字段从契约里拿掉，模型才不会"顺手"给出一份没人要的推导。
const recordSchema = `{
  "questions": [
    {
      "temp_id": "题目的临时编号，字符串，用于子题关联母题",
      "parent_temp_id": "若本题是某道母题的小问，填母题的 temp_id，否则省略",
      "question_type": "single_choice | multiple_choice | fill_blank | true_false | essay",
      "stem": "题干内容，Markdown 格式，行内公式写成 $...$，独立公式写成 $$...$$，填空题的空位写成六个下划线 ______",
      "options": [ {"label": "A", "content": "选项内容"} ],
      "figure_box": "可选，[x,y,w,h]，归一化到 0~1，表示该题在当前页面中的图形区域；没有题图则省略",
      "answer": "固定输出空字符串：本模式不求解，答案由老师填写",
      "difficulty": 1,
      "knowledge_points": ["涉及的知识点名称"],
      "tags": ["建议的标签"]
    }
  ]
}`

// ---- 可配置提示词 ----
//
// 本文件是全系统"提示词"的唯一真相源：有哪些提示词、每个支持哪些占位符、
// 没配置时用什么内置默认，都写在这里。数据库里的模板只是覆盖层——
// 删光所有模板，系统必须退回到本文件的常量，而不是退化成空提示词。

// 提示词键。存进 prompt_templates.key，也用于界面分组。
const (
	// PromptKeyImportExtract 智能录题·从材料中抽取题目。
	PromptKeyImportExtract = "import_extract"
	// PromptKeyImportSolve 智能录题·为已有题目补写答案与解析。
	PromptKeyImportSolve = "import_solve"
	// PromptKeyImportRecord 智能录题·只识别题目，不求解。
	PromptKeyImportRecord = "import_record"
	// PromptKeyAssistant AI 助手的系统提示词（人设与行为约束）。
	PromptKeyAssistant = "chat_assistant"
)

// Source 提示词的来源，即解析链上"最终是谁赢了"。
type Source string

const (
	// SourceSubject 该学科的覆盖模板。
	SourceSubject Source = "subject"
	// SourceGlobal 全局模板（「提示词」页保存的那条）。
	SourceGlobal Source = "global"
	// SourceSetting 系统设置里的兜底正文（仅录题的两个键）。
	SourceSetting Source = "setting"
	// SourceBuiltin 代码内置默认：什么都没配，或配的东西不可用。
	SourceBuiltin Source = "builtin"
)

// Placeholder 占位符说明，直接喂给管理界面。
type Placeholder struct {
	// Name 占位符名，不含大括号，如 subject_name。
	Name string
	// Desc 中文说明，写清楚"这里会被替换成什么"。
	Desc string
	// Optional 为真表示取值为空时会连带删掉整行。
	Optional bool
}

// PromptSpec 单个提示词的静态元数据。
type PromptSpec struct {
	Key         string
	Name        string
	Scene       string
	Description string
	// Placeholders 该提示词支持的占位符。写不存在的占位符不会报错，
	// 只会原样留在提示词里——所以这份清单同时是给管理员看的文档。
	Placeholders []Placeholder
	// SettingKey 全局兜底用的系统设置键；空表示没有对应的历史键。
	//
	// 这两个键（ai_extract_prompt / ai_solve_prompt）曾经声明了却无人读取，
	// 现在接在解析链的最后一层：只有既没有学科覆盖、也没有全局模板时才会读到。
	// 优先级刻意压到最低——模板表支持按学科覆盖、还能在界面上编辑，
	// 那里才是管理员该去的地方。
	SettingKey string
	// SchemaAware 为真表示输出结构是解析器的契约：
	// 模板里没写 {{output_schema}} 时也会自动补到末尾。
	//
	// 不补的后果不是报错，而是抽取质量断崖式下降（模型不知道该输出什么形状），
	// 而这种"沉默的劣化"比直接失败更难发现，因此宁可多写一段。
	SchemaAware bool
	// Default 内置默认模板正文，保存时带 {{占位符}}。
	Default string
}

// importPlaceholders 录题两个提示词共用的占位符。
var importPlaceholders = []Placeholder{
	{Name: "subject_name", Desc: "当前学科名。没有学科上下文时为空。"},
	{Name: "knowledge_point_block", Desc: "该学科已有知识点的清单块（自带引导语），引导模型优先复用而不是新造。", Optional: true},
	{Name: "tag_block", Desc: "该学科已有标签的清单块（自带引导语）。", Optional: true},
	{Name: "output_schema", Desc: "输出 JSON 结构说明。抽取与解题两种模式的结构不同。"},
}

// assistantPlaceholders 助手提示词支持的占位符。
var assistantPlaceholders = []Placeholder{
	{Name: "subject_name", Desc: "当前对话所属学科名。跨学科或未选学科时为空。"},
	{Name: "subject_description", Desc: "当前学科的说明，形如「（本学科说明：…）」。学科没填说明时为空。"},
	{Name: "scene_hint", Desc: "当前页面场景的一段提示（题库页/组卷页/录题审阅页）。识别不出场景时为空。", Optional: true},
}

// promptSpecs 全部可配置提示词。
//
// 只列系统里**真的存在**的提示词。新增一条时必须同时给出 Default：
// 没有内置默认的提示词一旦配置失效就没有兜底，那正是
// "存了却读不到"的另一种形式。
var promptSpecs = []PromptSpec{
	{
		Key:          PromptKeyImportExtract,
		Name:         "智能录题 · 题目抽取",
		Scene:        "import",
		Description:  "从试卷、讲义、教案等材料中识别出全部题目并整理成结构化数据。",
		Placeholders: importPlaceholders,
		SettingKey:   model.SettingAIExtractPrompt,
		SchemaAware:  true,
		Default:      defaultExtractTemplate,
	},
	{
		Key:          PromptKeyImportSolve,
		Name:         "智能录题 · 答案与解析",
		Scene:        "import",
		Description:  "题目已经存在时，让模型补写答案与解析，题干原样返回。",
		Placeholders: importPlaceholders,
		SettingKey:   model.SettingAISolvePrompt,
		SchemaAware:  true,
		Default:      defaultSolveTemplate,
	},
	{
		Key:         PromptKeyImportRecord,
		Name:        "智能录题 · 仅录题",
		Scene:       "import",
		Description: "只从材料里识别题目本身，不要答案与解析——省下求解的输出与等待。",
		// 没有 SettingKey：这一条是新增的，不存在对应的历史系统设置键。
		// 造一个永远没人写入的键只会让「系统设置」页多一行死配置。
		Placeholders: importPlaceholders,
		SchemaAware:  true,
		Default:      defaultRecordTemplate,
	},
	{
		Key:          PromptKeyAssistant,
		Name:         "AI 助手 · 系统提示词",
		Scene:        "chat",
		Description:  "助手的身份、工具使用纪律与回答风格。",
		Placeholders: assistantPlaceholders,
		Default:      defaultAssistantTemplate,
	},
}

// PromptSpecs 返回全部提示词定义的副本。
func PromptSpecs() []PromptSpec {
	out := make([]PromptSpec, len(promptSpecs))
	copy(out, promptSpecs)
	return out
}

// SpecOf 按键取提示词定义。
func SpecOf(key string) (PromptSpec, bool) {
	for _, s := range promptSpecs {
		if s.Key == key {
			return s, true
		}
	}
	return PromptSpec{}, false
}

// PromptKeyOfMode 导入模式对应的提示词键。
func PromptKeyOfMode(m Mode) string {
	switch m {
	case ModeSolve:
		return PromptKeyImportSolve
	case ModeRecord:
		return PromptKeyImportRecord
	}
	return PromptKeyImportExtract
}

// ---- 内置默认模板 ----
//
// 这些常量是"没有任何配置"时的最终兜底。正文与改造前的硬编码提示词逐字一致，
// 只是把随请求变化的部分换成了占位符——因此把数据库清空之后，
// 系统行为与改造前完全一致，不会因为"加了提示词功能"而让抽取结果变形。

const defaultExtractTemplate = `你是一位资深的中小学与大学学科教研员，擅长把试卷、讲义、教案等材料整理成结构化的题库数据。

任务：从下面的材料中识别出所有题目，并整理成结构化数据。
注意：
1. 材料里可能有标题、考号说明、分数说明等非题目内容，这些不要当成题目。
2. 每道题必须包含题干；如果材料里有选项、答案、解析，一并提取。
3. 如果某道题没有给出答案，answer 留空字符串，不要自己编造。
4. 材料里出现的小题（如「(1)」「(2)」）作为母题的小问处理，用 parent_temp_id 关联。
5. 如果材料按「选择题」「填空题」等题型分节，题型必须服从所在小节；多选小节中的题不要标成单选。
6. 只有同一道题的小问才用 parent_temp_id；连续编号的不同题目即使共用阅读材料，也不要建立父子关系。
7. 题号照原卷填写；题干只保留该题自身内容，不要混入「阅读 I」「材料一」这类版面标题。
8. 如果题干提到「如图」「图象」等且题目确实配有图形，请给出 figure_box；图形只属于母题时，子题不要重复给出。

数学公式必须使用 LaTeX：行内用 $...$，独立成行用 $$...$$。
例如：函数 $f(x) = \frac{1}{2}x^2$ 在 $x=1$ 处的导数为 $f'(1)$。
不要使用 Unicode 数学符号（如 ×÷≤），一律用 LaTeX 命令（\times \div \leq）。

填空题的空位统一写成六个下划线：______ 。答案里同一个空的多个可接受写法用「或」分隔。
本材料属于学科：{{subject_name}}
{{knowledge_point_block}}
{{tag_block}}
严格只输出一个 JSON 对象，不要输出任何解释文字，也不要用 Markdown 代码块包裹。
输出结构如下（这是结构说明，不是示例数据）：
{{output_schema}}`

const defaultSolveTemplate = `你是一位资深的中小学与大学学科教研员，擅长把试卷、讲义、教案等材料整理成结构化的题库数据。

任务：为下面给出的每一道题补写答案与解析。题干必须原样返回，不要改写、不要增删题号。
题目的**题型已由系统给定**（可能标在材料开头），不要自行判断或更改题型。
材料里若列出了选项，那些选项与题干同属一道题，**必须结合选项作答**；
选择题的答案只写选项字母，不要复述选项内容。

数学公式必须使用 LaTeX：行内用 $...$，独立成行用 $$...$$。
例如：函数 $f(x) = \frac{1}{2}x^2$ 在 $x=1$ 处的导数为 $f'(1)$。
不要使用 Unicode 数学符号（如 ×÷≤），一律用 LaTeX 命令（\times \div \leq）。

填空题的空位统一写成六个下划线：______ 。答案里同一个空的多个可接受写法用「或」分隔。
本材料属于学科：{{subject_name}}
{{knowledge_point_block}}
{{tag_block}}
严格只输出一个 JSON 对象，不要输出任何解释文字，也不要用 Markdown 代码块包裹。
输出结构如下（这是结构说明，不是示例数据）：
{{output_schema}}`

const defaultRecordTemplate = `你是一位资深的中小学与大学学科教研员，擅长把试卷、讲义、教案等材料整理成结构化的题库数据。

任务：从下面的材料中识别出所有题目，**只记录题目本身**。
注意：
1. 材料里可能有标题、考号说明、分数说明等非题目内容，这些不要当成题目。
2. 每道题必须包含题干；材料里有选项、知识点提示时一并提取。
3. **不要解题**：不写答案，也不写解析。材料里印着答案或解析时同样留空——
   答案与解析由老师事后填写，你写出来的会被丢弃，却让整份识别多花几倍时间与费用。
4. 材料里出现的小题（如「(1)」「(2)」）作为母题的小问处理，用 parent_temp_id 关联。
5. 如果材料按「选择题」「填空题」等题型分节，题型必须服从所在小节；多选小节中的题不要标成单选。
6. 只有同一道题的小问才用 parent_temp_id；连续编号的不同题目即使共用阅读材料，也不要建立父子关系。
7. 题号照原卷填写；题干只保留该题自身内容，不要混入「阅读 I」「材料一」这类版面标题。
8. 如果题干提到「如图」「图象」等且题目确实配有图形，请给出 figure_box；图形只属于母题时，子题不要重复给出。

数学公式必须使用 LaTeX：行内用 $...$，独立成行用 $$...$$。
例如：函数 $f(x) = \frac{1}{2}x^2$ 在 $x=1$ 处的导数为 $f'(1)$。
不要使用 Unicode 数学符号（如 ×÷≤），一律用 LaTeX 命令（\times \div \leq）。

填空题的空位统一写成六个下划线：______ 。
本材料属于学科：{{subject_name}}
{{knowledge_point_block}}
{{tag_block}}
严格只输出一个 JSON 对象，不要输出任何解释文字，也不要用 Markdown 代码块包裹。
输出结构如下（这是结构说明，不是示例数据）：
{{output_schema}}`

const defaultAssistantTemplate = `你是 QuestionMind 题库系统的 AI 助手，帮助教师管理题目和组卷。

工作方式：
1. 需要题库信息时先调用工具查，不要凭空编造题目 id 或内容。
2. 工具返回错误时读懂原因再调整，不要原样重试同一个调用。
3. 改动数据的工具有 dry_run 参数，省略就是预演。第一次调用一定要用预演，
   把「会改哪几道题、哪几道改不了」说给用户听，得到确认后再用 dry_run=false 执行。
4. 破坏性操作（删除、批量归档、批量审核）即使预演通过了，也要先问一句是不是真的要做。
5. 一次工具调用做完一件事就停下来向用户汇报，不要连着发几次调用。
6. 回答简洁直接，用中文；涉及公式时用 LaTeX，行内写 $...$。
7. 找不到合适内容时如实说明，不要勉强凑数。
{{subject_name}}{{subject_description}}
{{scene_hint}}`

// ---- 渲染 ----

// placeholderPattern 匹配 {{name}}。
//
// 用双大括号而不是 {name}：提示词正文里满是 JSON 与 LaTeX（\frac{1}{2}），
// 单大括号会和这些内容撞车，双大括号在自然语言与代码里都极少出现。
var placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// outputSchemaToken 输出结构占位符的名字，见 importPlaceholders。
const outputSchemaToken = "output_schema"

// maxPromptRunes 单条提示词的长度上限。
//
// 它不是安全边界，是"管理员是不是粘贴错了什么"的兜底：几十万字的提示词
// 会挤爆上下文、把成本抬高一个数量级，而且几乎一定是误操作。
// 超限按不可用处理、回落到内置默认，而不是截断后送给模型
// （半截的提示词比默认的更糟）。
const maxPromptRunes = 120_000

// Vars 一次渲染可用的变量取值。
type Vars map[string]string

// Render 把模板中的占位符替换成实际取值。
//
// 规则只有一条，但它决定了模板该怎么写：
//
//	一行里只要有一个占位符取值为空，整行就被删除。
//
// 这是"可选段落"的实现方式——{{knowledge_point_block}} 为空时，
// 连同引导模型的那一整段一起消失，而不是在提示词里留下一个空标题。
// 代价是：必须保留的固定文字不能和可能为空的占位符写在同一行，
// 这条约束会在管理界面上原样写给管理员。
//
// 未被识别的占位符原样保留、也不参与"整行删除"的判定。它们多半是管理员
// 拼错了，留着比悄悄替换成空更容易被发现（模型会照着念出来），
// 整行删除则会让"为什么这段没生效"变成一件查不到的事。
func Render(tmpl string, vars Vars) string {
	lines := strings.Split(tmpl, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if rendered, keep := renderLine(line, vars); keep {
			out = append(out, rendered)
		}
	}
	return strings.Join(out, "\n")
}

// renderLine 渲染单行，第二个返回值表示这一行是否保留。
func renderLine(line string, vars Vars) (string, bool) {
	matches := placeholderPattern.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return line, true
	}

	var sb strings.Builder
	last := 0
	allEmpty := true
	for _, m := range matches {
		name := line[m[2]:m[3]]
		value, known := vars[name]
		if !known {
			allEmpty = false
			continue
		}
		if strings.TrimSpace(value) != "" {
			allEmpty = false
		}
		sb.WriteString(line[last:m[0]])
		sb.WriteString(value)
		last = m[1]
	}
	sb.WriteString(line[last:])

	if allEmpty {
		return "", false
	}
	return sb.String(), true
}

// Usable 判断一段渲染结果能不能拿去喂模型。
//
// 判空是必须的：模板可能整个就是 "{{knowledge_point_block}}"，
// 在没有知识点的学科上它会渲染成空串。空系统提示词会让模型完全放飞，
// 比用默认提示词糟得多，所以这种情况一律回落到内置默认。
func Usable(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && utf8.RuneCountInString(t) <= maxPromptRunes
}

// SampleVars 按占位符清单生成一份"每个占位符都有值"的取值。
//
// 它只有一个用途：在**没有真实上下文**时（管理界面点开一条刚保存的模板，
// 手里既没有学科也没有知识点清单）判断这段模板是不是**无论如何都**渲染不出东西。
// 用真实取值判断会误报——"{{subject_name}}" 在没有学科的会话里是空的，
// 但它是一条好模板。
func SampleVars(spec PromptSpec) Vars {
	out := EmptyVars(spec)
	for _, p := range spec.Placeholders {
		out[p.Name] = "（示例）" + p.Name
	}
	if spec.SchemaAware {
		out[outputSchemaToken] = extractSchema
	}
	return out
}

// EmptyVars 返回一份"每个占位符都有键、但取值都是空串"的 Vars。
//
// 与"什么都不给"是两件事：缺键会被当成未知占位符原样保留，
// 而空值会触发"整行删除"。判断"这条模板在没有任何上下文时会变成什么"
// 必须用这个，否则结论是反的。
func EmptyVars(spec PromptSpec) Vars {
	out := make(Vars, len(spec.Placeholders)+1)
	for _, p := range spec.Placeholders {
		out[p.Name] = ""
	}
	if spec.SchemaAware {
		out[outputSchemaToken] = ""
	}
	return out
}

// ExplainTemplate 判定一段模板在给定取值下能不能用；不可用时给出人话原因。
//
// 解析链与管理界面共用这一条规则。两处各判一次，早晚会判出不一样的结果，
// 到时候界面显示"可用"、运行时却悄悄回落到默认，又是那种查不到的问题。
func ExplainTemplate(tmpl string, vars Vars) (bool, string) {
	if strings.TrimSpace(tmpl) == "" {
		return false, "模板正文为空"
	}
	if !Usable(Render(tmpl, vars)) {
		return false, "模板渲染后为空（所有占位符都取不到值），或超出长度上限"
	}
	return true, ""
}

// ---- 模板存储 ----

// TemplateHit 命中的模板。
type TemplateHit struct {
	Body   string
	Source Source
	// TemplateID 命中的模板行 id；来自系统设置时为 0。
	TemplateID uint
	SubjectID  *uint
}

// TemplateStore 提示词模板的只读视图。
//
// 由 service 层的 PromptService 实现（它同时看得到模板表与系统设置表），
// importing 只声明"我需要什么"，不关心存在哪儿——这样领域层不必依赖
// 存储实现，测试也能塞一个内存实现进去。
type TemplateStore interface {
	LookupTemplate(ctx context.Context, key string, subjectID *uint) (TemplateHit, error)
}

type storeHolder struct{ store TemplateStore }

// currentStore 全局模板源。
//
// 用 atomic.Pointer 而不是普通变量：它会在每次抽取、每轮对话时被读到，
// 而装配只发生一次。宁可多一个间接层，也不要在读路径上拿锁。
var currentStore atomic.Pointer[storeHolder]

// UseTemplateStore 注入模板源，由 service.NewPromptService 在装配时调用一次。
//
// 没注入时（单元测试、独立工具程序）解析链会一直落到内置默认——
// 这正是"没配置"的语义，不需要为此报错。
func UseTemplateStore(s TemplateStore) {
	if s == nil {
		return
	}
	currentStore.Store(&storeHolder{store: s})
}

// ---- 解析链 ----

// Resolution 一次解析的结果。
type Resolution struct {
	Key    string
	Text   string
	Source Source
	// TemplateID 命中的模板行 id；来自内置默认或系统设置时为 0。
	TemplateID uint
	// SubjectID 命中的学科覆盖；非学科来源时为 nil。
	SubjectID *uint
	// Fallback 非空说明为什么没用配置值——管理界面用它解释
	// "我明明配了模板，为什么界面上显示的不是它"。
	Fallback string
}

// ResolveSystemPrompt 按「学科覆盖 → 全局模板 → 系统设置兜底 → 内置默认」
// 取提示词并渲染。
//
// 这条链子上**任何一步失败都只降级、不报错**：读不到模板、模板是空的、
// 渲染出空串、模板长到离谱——统统回落到内置默认。一次导入要跑 30 秒到
// 3 分钟，为了一段提示词把它打断是本末倒置。
func ResolveSystemPrompt(ctx context.Context, key string, subjectID *uint, vars Vars) Resolution {
	spec, ok := SpecOf(key)
	if !ok {
		// 调用方传了目录里没有的键：这是程序错误而不是配置错误。
		// 即便如此也不能让导入失败——留下日志，交给上层按"没有提示词"处理。
		slog.Error("未知的提示词键", "key", key)
		return Resolution{Key: key, Source: SourceBuiltin, Fallback: "未知的提示词键"}
	}

	tmpl, hit := lookupTemplate(ctx, spec, subjectID)
	source := hit.Source
	templateID := hit.TemplateID
	fallback := ""

	if strings.TrimSpace(tmpl) == "" {
		tmpl = spec.Default
		source = SourceBuiltin
		templateID = 0
		fallback = "没有配置模板，使用内置默认"
	}

	text := Render(tmpl, vars)
	if ok, reason := ExplainTemplate(tmpl, vars); !ok {
		// 配了，但渲染不出能用的东西。这比"没配"更危险：管理员会以为
		// 自己已经接管了提示词。因此明确降级，并把原因带出去。
		slog.Warn("提示词模板不可用，回落到内置默认",
			"key", key, "source", source, "template_id", templateID, "reason", reason)
		text = Render(spec.Default, vars)
		source = SourceBuiltin
		templateID = 0
		fallback = reason + "，已回落到内置默认"
	}

	if spec.SchemaAware && !strings.Contains(tmpl, "{{"+outputSchemaToken+"}}") {
		// 模板没引用输出结构占位符：补在末尾。宁可多一段，
		// 也不要让模型面对一个没有输出约定的抽取任务。
		text = strings.TrimRight(text, "\n") + "\n\n" + vars[outputSchemaToken]
		fallback = joinFallback(fallback, "模板未引用 "+outputSchemaToken+"，已自动补上输出结构")
	}

	return Resolution{
		Key:        key,
		Text:       text,
		Source:     source,
		TemplateID: templateID,
		SubjectID:  hit.SubjectID,
		Fallback:   fallback,
	}
}

// joinFallback 合并多条降级说明。
func joinFallback(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

// lookupTemplate 取配置值。失败时返回空命中，由调用方落到内置默认。
func lookupTemplate(ctx context.Context, spec PromptSpec, subjectID *uint) (string, TemplateHit) {
	h := currentStore.Load()
	if h == nil || h.store == nil {
		return "", TemplateHit{Source: SourceBuiltin}
	}

	hit, err := h.store.LookupTemplate(ctx, spec.Key, subjectID)
	if err != nil || strings.TrimSpace(hit.Body) == "" {
		if err != nil {
			slog.Warn("读取提示词模板失败，使用内置默认", "key", spec.Key, "error", err)
		}
		return "", TemplateHit{Source: SourceBuiltin}
	}
	if hit.Source == "" {
		hit.Source = SourceGlobal
	}
	return hit.Body, hit
}

// ---- 智能录题提示词组装 ----

// PromptOptions 生成提示词的参数。
type PromptOptions struct {
	Mode Mode
	// SubjectName 学科名，帮助模型理解上下文。
	SubjectName string
	// SubjectID 学科 id。决定能否命中该学科的提示词覆盖。
	//
	// 调用方必须真的把它填上：留空不会报错，只会安静地退回全局模板，
	// 而管理员在界面上明明看到这个学科配了覆盖。凡是走智能录题的入口
	// 都应带上它（service/import.go 组装 PromptOptions 的地方）。
	SubjectID *uint
	// ExistingKnowledgePoints 已有知识点，供模型优先复用而不是新造。
	ExistingKnowledgePoints []string
	// ExistingTags 已有标签。
	ExistingTags []string
	// AnswerImages 本次随材料一起提交的图片数量。
	//
	// 用来改写提示词：图片在这里是**答案页**，模型该做的是转写而不是解题。
	// 这个差别是巨大的——没有答案页时，模型要为每道解答题写出完整推导
	// （实测约 1370 输出 token/题，整卷近三万 token，几分钟起步）；
	// 有答案页时它只需要抄，比自己算快一个数量级。
	// 仅 ModeExtract 会用到它：另两种模式不碰答案页。
	AnswerImages int
	// AssetImages 本次随材料提交的文档内嵌图片数量。
	AssetImages int
	// PageImages 本次随材料提交的**题目页**图片数量（PDF 渲染出来的页图）。
	//
	// 与 AnswerImages 必须分开：前者是"卷子长什么样"，要照着它识别题目；
	// 后者是"答案写在哪"，要转写它。混为一谈时模型会把题目页当答案页照抄，
	// 于是题库里多出一批"题目 + 原卷答案"的草稿。
	PageImages int
}

// lookupTimeout 读模板的超时。
const lookupTimeout = 3 * time.Second

// BuildSystemPrompt 生成智能录题的系统提示词。
//
// 解析链：学科覆盖 → 全局模板 → 系统设置兜底 → 内置默认。
// 最后一环永远有值，所以这个函数没有错误返回。
func BuildSystemPrompt(opt PromptOptions) string {
	// 这里自己造 ctx：调用方（service/import.go 的 Extract）没有可传入的
	// 上下文，而这一层又不能去改它的签名。给一个明确的超时，
	// 免得数据库卡住时把整次导入一起拖死。
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()

	return ResolveSystemPrompt(ctx, PromptKeyOfMode(opt.Mode), opt.SubjectID, importVars(opt)).Text
}

// importVars 组装录题提示词的占位符取值。
func importVars(opt PromptOptions) Vars {
	schema := extractSchema
	switch opt.Mode {
	case ModeSolve:
		schema = solveSchema
	case ModeRecord:
		schema = recordSchema
	}
	return Vars{
		"subject_name":          opt.SubjectName,
		"knowledge_point_block": knowledgePointBlock(opt.ExistingKnowledgePoints),
		"tag_block":             tagBlock(opt.ExistingTags),
		outputSchemaToken:       schema,
	}
}

// knowledgePointBlock 把已有知识点拼成一段带引导语的清单。
func knowledgePointBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("知识点请优先从下列已有清单中选取（原样使用名称，不要改写）：\n")
	sb.WriteString("  " + strings.Join(limitStrings(names, 200), "、") + "\n")
	sb.WriteString("清单里确实没有合适的，才新建知识点名称。")
	return sb.String()
}

// tagBlock 把已有标签拼成一段带引导语的清单。
func tagBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return "标签请优先从下列已有清单中选取：\n  " + strings.Join(limitStrings(names, 100), "、")
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
	var b strings.Builder

	switch opt.Mode {
	case ModeSolve:
		b.WriteString("请为下列题目补写答案与解析：\n\n")
	case ModeRecord:
		b.WriteString("请从下列材料中提取题目，只记录题目本身，不要给出答案与解析：\n\n")
	default:
		b.WriteString("请从下列材料中提取题目：\n\n")
	}
	if content != "" {
		b.WriteString(content)
		b.WriteString("\n\n")
	}

	// 有答案页时把用法说清楚。不说，模型很可能仍然把图片当成
	// "补充插图"而去自己解题——于是图片白传，耗时一分没省。
	//
	// 仅录题是这条提示的唯一例外：它要的就是不碰答案，
	// 告诉模型去"转写答案页"会让它重新干回求解的活。
	if opt.AnswerImages > 0 && opt.Mode == ModeExtract {
		b.WriteString(answerImagesHint(opt.AnswerImages))
	}

	// 页图说明放在答案页之后：它描述的是"材料本体"，
	// 而答案页说明是"另有 N 张图别当成题目"，后出现的更靠近模型看到图片的那一刻。
	if opt.PageImages > 0 {
		b.WriteString(pageImagesHint(opt.PageImages))
	}
	if opt.AssetImages > 0 {
		b.WriteString(assetImagesHint(opt.AssetImages))
	}
	return strings.TrimSpace(b.String())
}

// pageImagesHint 告诉模型这批图是卷面的页面本身。
//
// 不说的话风险很具体：模型看到 N 张试卷页却没有别的说明，
// 可能把它们当成 N 道题的插图逐张描述，也可能只认真读第一张。
// 把"页码顺序""一张图里有多道题""公式照抄排版"讲清楚，
// 比事后在草稿里逐条核对便宜得多。
func pageImagesHint(n int) string {
	var b strings.Builder
	b.WriteString("本次材料的原文以 ")
	b.WriteString(itoa(n))
	b.WriteString(" 张**页面截图**的形式给出（按页码顺序，卷面即原样，未做文字化处理）。\n")
	b.WriteString("请按下列规则处理：\n")
	b.WriteString("1. 逐页阅读，把每一页上的**所有**题目都抽出来，一张图里通常有多道题；\n")
	b.WriteString("2. 题干、选项里的数字与公式必须与图上完全一致，公式写成 $...$（LaTeX）；\n")
	b.WriteString("3. 题号、大题标题与题间说明文字用 outline 之外的文字原样带出，便于还原卷面；\n")
	b.WriteString("4. 图上没有的题目不要编——宁可少抽，也不要凭空生成。\n")
	return b.String()
}

// assetImagesHint 告诉模型文档内嵌图片与正文中的图片引用是对应的。
func assetImagesHint(n int) string {
	var b strings.Builder
	b.WriteString("另外随本次材料附了 ")
	b.WriteString(itoa(n))
	b.WriteString(" 张文档内嵌图片，它们在正文中对应 ![图片](/media/...) 引用。\n")
	b.WriteString("请结合图片内容理解题目，并在对应题干中原样保留图片引用；不要解释、删除或改写这些引用。")
	return b.String()
}

// answerImagesHint 告诉模型随附图片该怎么用。
func answerImagesHint(n int) string {
	var b strings.Builder
	b.WriteString("另外随本次材料附了 ")
	b.WriteString(itoa(n))
	b.WriteString(" 张图片，是同一份试卷的**答案与解析页**（不是题目插图）。\n")
	b.WriteString("请按下列规则处理：\n")
	b.WriteString("1. 先读图片，从里面取出与每道题对应的答案与解析；\n")
	b.WriteString("2. 对得上号的题目，答案与解析直接**照抄**图片上的内容，不要自己重算；\n")
	b.WriteString("3. 图片里对不上的题目（答案页缺页、或图片与本题无关），answer 与 analysis 留空，")
	b.WriteString("并在 warnings 里写明「答案页未见此题」——宁可空着，也不要编一个答案。\n")
	return b.String()
}

// DescribeSubject 学科说明的占位符格式。
//
// 学科没填说明时返回空串，而不是「（本学科说明：）」这种空壳——
// 空壳会被原样拼进提示词，模型会以为有一条空的学科说明。
func DescribeSubject(description string) string {
	desc := strings.TrimSpace(description)
	if desc == "" {
		return ""
	}
	return "（本学科说明：" + desc + "）"
}
