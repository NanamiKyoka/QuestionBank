package importing

import (
	"regexp"
	"strings"
)

// DocumentSections 是题目区与答案区的文本切片。
type DocumentSections struct {
	Questions string
	Answers   string
}

var (
	answerRunPattern  = regexp.MustCompile(`(?:^|[\s])([1-9]\d{0,2})\s*[.、．]\s*([A-H])`)
	answerMarkPattern = regexp.MustCompile(`^\s*(?:【(?:参考答案|答案|解析|详解|导语|参考译文|译文|范文|例文)】|(?:参考答案|答案|解析|详解|导语|参考译文|译文|范文|例文)\s*[:：])`)
)

// SeparateDocumentSections 在题目区结束后定位答案区。
func SeparateDocumentSections(content string) DocumentSections {
	if strings.TrimSpace(content) == "" {
		return DocumentSections{}
	}

	lines := strings.Split(content, "\n")
	questionSeen := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if questionStartRe.MatchString(trimmed) {
			questionSeen = true
		}
		if questionSeen && isAnswerBoundary(trimmed) {
			return DocumentSections{
				Questions: strings.TrimSpace(strings.Join(lines[:i], "\n")),
				Answers:   strings.TrimSpace(strings.Join(lines[i:], "\n")),
			}
		}
	}
	return DocumentSections{Questions: strings.TrimSpace(content)}
}

func isAnswerBoundary(line string) bool {
	if answerMarkPattern.MatchString(line) {
		return true
	}
	return len(answerRunPattern.FindAllStringSubmatch(line, -1)) >= 2
}

// SplitForExtractionMode 根据录题模式决定输入范围。
func SplitForExtractionMode(content string, targetChunks int, mode Mode) []Chunk {
	if mode == ModeRecord {
		content = SeparateDocumentSections(content).Questions
	}
	return splitForExtraction(content, targetChunks)
}
