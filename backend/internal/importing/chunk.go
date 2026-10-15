package importing

import (
	"regexp"
	"strings"
)

// 把一份文档切成若干块、并发送模型再拼回来。
//
// 为什么值得这么做：一次调用处理整份卷子，耗时几乎全部花在**输出**
// 上——模型要逐题吐出结构化 JSON（题干、选项、答案、解析）。
// 一份二十道题的卷子动辄上万输出 token，而模型的输出速度是固定的，
// 于是"整份一次调用"的墙钟时间就是那份输出所需的时间，压不下去。
//
// 切成四份并发跑，总输出量不变，墙钟时间约降到原来的 1/4。
// 代价是每次调用看不到全卷，因此切分必须守住两条线：
//   - 绝不把一道题从中间劈开；
//   - 每块都带上它所属的大题标题，否则模型不知道自己在答填空题还是选择题。
//
// 不分块的情况：小文档一次调用本来��要几秒，切成四块只会多出三次往返
// 和四份系统提示词。

// ExtractionConcurrency 同时进行的抽取调用数。
//
// 取 4 而不是更高：再往上多半换来的是供应商的速率限制，而速率限制的
// 表现是"退避重试"，总耗时反而比 4 并发更长——指数退避会把省下来的
// 时间全吃掉。
const ExtractionConcurrency = 4

const (
	// extractionSplitThreshold 短于这个长度就不切。
	//
	// 一道大题连题干带解析差不多这个量级：再往下切，一次调用本来
	// 只要几秒，切开反而更慢。
	extractionSplitThreshold = 4000

	// extractionChunkTarget 每块的目标**字节数**，只当作兜底。
	//
	// 真正决定单块耗时的是**输出量**，而输出量由题数决定、由模型自由发挥
	// （实测约 1370 输出 token/题，因为它会给每道题写完整解答）。
	// 所以下面还有一道按题数切的闸门，字节数只防"一大段没有题号的文字"。
	extractionChunkTarget = 2200

	// extractionMaxQuestionsPerChunk 单块最多几道题。
	//
	// 取 8 是量出来的：8 道题 ≈ 1.1 万输出 token，在实测的 71.5 token/s
	// 下约 150 秒——稳稳落在上游 ~300 秒的硬上限之内。
	// 放到 12 就越界了：1200 字节 / 11 道题那一档实测正好在 211 秒，
	// 再多就会看到 "Upstream model provider is temporarily unavailable"。
	//
	// 这不是拍脑袋的缓冲，是"宁可多发几次请求，也不要整卷失败"——
	// 上游超时返回的是 500 而不是 429，用户看不出该重试还是该换模型。
	extractionMaxQuestionsPerChunk = 8

	// extractionChunkMax 单块的硬上限（字节）。
	//
	// 切点只在"新题开头"上找，但一段没有题号的长文字（比如整页的阅读
	// 材料）可能顶到几十 KB，这时必须在行边界上强切，
	// 否则单块会撑爆上下文窗口。
	extractionChunkMax = 8000
)

// 切分点：只认这几种行首标记。
//
// 刻意**不认**"（1）"这类小问——切一刀就把材料题和它的小问分到两块，
// 而 parent_temp_id 是跨块关联的；分开之后那道母题会变成一道孤立的题，
// 外加十几道失去归属的小问。宁可让某一块偏大。
var (
	// 中文大题标题：一、选择题（本大题共 4 题）
	sectionStartRe = regexp.MustCompile(`^\s*[一二三四五六七八九十]+\s*[、.．]`)

	// 阿拉伯数字题号：1. / 12．/ 13）
	//
	// 不含"（1）"形式，理由见上。
	questionStartRe = regexp.MustCompile(`^\s{0,8}\d{1,3}\s*[.、．]`)

	// 阅读/材料组标题：一、阅读下的“（一）阅读 I”等。
	groupStartRe = regexp.MustCompile(`^\s*[（(]\s*[一二三四五六七八九十]+\s*[）)]\s*(?:阅读|现代文阅读|文言文阅读|古代诗歌阅读|语言文字运用|写作)`)
)

// Chunk 是一段待抽取的内容，外加它所属的大题标题。
type Chunk struct {
	// Text 是真正发给模型的内容（已带上标题前缀）。
	Text string
	// Heading 是本段所属的大题标题；空串表示没有。
	Heading string
	// Group 是本段所属的阅读/材料组标题；空串表示没有。
	Group string
	// Label 面向用户的短描述，用于失败时说明"缺了哪一段"。
	Label string
}

// SplitForExtraction 把文档切成适合并发抽取的块。
//
// targetChunks <= 0 表示由长度自行决定块数。返回的块保持原顺序，
// 拼接起来覆盖全部内容（刻意丢弃的空行除外）。
func SplitForExtraction(content string, targetChunks int) []Chunk {
	return splitForExtraction(content, targetChunks)
}

func splitForExtraction(content string, targetChunks int) []Chunk {
	lines := splitLines(content)
	if len(lines) == 0 {
		return nil
	}
	if len(content) < extractionSplitThreshold {
		return []Chunk{{Text: content, Label: DescribeChunk(content)}}
	}

	bySize := len(content) / extractionChunkTarget
	if bySize < 2 {
		bySize = 2
	}
	if targetChunks > bySize {
		bySize = targetChunks
	}
	perChunk := len(content) / bySize

	var (
		out    []Chunk
		cur    []string
		curLen int
		curQ   int // 本块已攒下的题数

		// heading 是当前看到的大题标题；chunkHeading 是**本块起点处**
		// 生效的那个（块中途换标题是常事，但块头那一条才是上下文）。
		heading      string
		chunkHeading string
		group        string
		chunkGroup   string
	)

	flush := func() {
		if len(cur) == 0 {
			return
		}
		body := strings.Join(cur, "\n")
		cur = cur[:0]
		curLen, curQ = 0, 0
		if strings.TrimSpace(body) == "" {
			return
		}
		text := body
		if chunkHeading != "" {
			text = "（本段属于：" + chunkHeading + "）\n" + body
		}
		if chunkGroup != "" && chunkGroup != chunkHeading {
			text = "（本段材料组：" + chunkGroup + "）\n" + text
		}
		out = append(out, Chunk{Text: text, Heading: chunkHeading, Group: chunkGroup, Label: DescribeChunk(body)})
		chunkHeading = ""
		chunkGroup = ""
	}

	for _, line := range lines {
		isSection := sectionStartRe.MatchString(line)
		isGroup := groupStartRe.MatchString(line)
		if (isSection || isGroup) && len(cur) > 0 && curQ > 0 {
			flush()
		}
		if isSection {
			heading = strings.TrimSpace(line)
			if !isGroup {
				group = ""
			}
		}
		if isGroup {
			group = strings.TrimSpace(line)
			if curQ == 0 {
				chunkGroup = group
			}
		}

		// 切点：站在新题开头，且这一块已经够大了。
		//
		// 两个闸门取"先到先算"：题数超了就切，字节数超了也切。
		// 题数是主闸门——它才对应输出量，也就是真正的耗时。
		// 标题本身**不**触发切分：它要跟着它下面的题走。
		startsQuestion := !isSection && questionStartRe.MatchString(line)
		if startsQuestion && len(cur) > 0 &&
			(curQ >= extractionMaxQuestionsPerChunk || curLen >= perChunk) {
			flush()
		}
		if len(cur) == 0 {
			chunkHeading = heading
			chunkGroup = group
		}
		if startsQuestion {
			curQ++
		}

		cur = append(cur, line)
		curLen += len(line) + 1

		// 硬上限：整块已经太长且现在正站在题号上，就在这里断开。
		if curLen >= extractionChunkMax && startsQuestion {
			flush()
		}
	}
	flush()

	return out
}

// splitLines 按行切开并丢掉纯空行。
//
// 空行对内容长度毫无贡献，却会让"每块目标长度"算不准：
// 一份排版松散的卷子可能有一半的行是空的。
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// DescribeChunk 用首行给一段内容起个名字。
//
// 失败时用户要知道"缺了哪一段"，光说"第 2 块失败"没有用——
// 他得看得见那段的题目是什么，才可能去单独补一次。
func DescribeChunk(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = stripLeadingNoise(line)
		line = strings.TrimSpace(line)
		if line == "" {
			continue // 注释行、空行都跳过，继续找第一行真正的内容
		}
		line = stripChunkPrefix(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxChunkLabel {
			return string(r[:maxChunkLabel]) + "…"
		}
		return line
	}
	return "(空)"
}

// maxChunkLabel 失败说明里显示多少字。
// 再长就该进详情页看了——一整行塞进提示框会把真正的原因挤出去。
const maxChunkLabel = 40

func stripChunkPrefix(line string) string {
	for _, prefix := range []string{"（本段属于：", "（本段材料组："} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if i := strings.IndexRune(line, '）'); i >= 0 {
			line = strings.TrimSpace(line[i+len("）"):])
		}
	}
	return line
}

// stripLeadingNoise 去掉一整行或行首的 HTML 注释。
func stripLeadingNoise(s string) string {
	for {
		t := strings.TrimSpace(s)
		if !strings.HasPrefix(t, "<!--") {
			return t
		}
		i := strings.Index(t, "-->")
		if i < 0 {
			return t
		}
		s = t[i+len("-->"):]
	}
}
