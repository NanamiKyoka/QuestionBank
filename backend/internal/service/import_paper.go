package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 整卷导入：把一次导入还原成一份可编辑的试卷。
//
// 为什么要单独做：导入题目只产出"有哪些题"，而一份试卷还包括
// 大题标题、题间说明、作答区。这些版面信息在解析阶段已经被记进大纲，
// 却从来不落库——丢掉之后就再也拼不回来了，而老师重排一份
// 两百题的卷子要花掉整个下午。
//
// 三条纪律：
//  1. 建卷失败绝不影响题目入库。题目已经落库并回传了 id，
//     丢一份排版远比丢几十道题轻。
//  2. 只引用**真正建成**的题。大纲里的 draft_index 可能指向一道
//     入库失败的题，此时跳过该节点并记下来——宁可卷子上少一道题，
//     也不要出现一个引用失效的空块。
//  3. 不编造版面。大纲里没有的标题、答题区一律不补。

// paperFromImport 按大纲创建试卷。
//
// createdIDOf 按**草稿下标**给出题目 id：0 表示那道题入库失败。
func (s *ImportService) paperFromImport(
	ctx context.Context, user *model.User, subjectID uint,
	req *schema.CommitRequest, createdIDOf []uint,
) (uint, error) {
	if s.papers == nil {
		return 0, apperr.New(apperr.CodeInternal, "整卷导入未启用")
	}
	if len(createdIDOf) == 0 {
		return 0, apperr.New(apperr.CodeUnprocessable, "本次没有题目入库，未创建试卷")
	}

	title := strings.TrimSpace(req.PaperTitle)
	if title == "" {
		title = strings.TrimSpace(req.Filename)
	}
	if title == "" {
		title = "导入试卷 " + time.Now().Format("2006-01-02")
	}

	detail, err := s.papers.CreatePaper(ctx, user, subjectID, &schema.PaperCreateRequest{
		Title: title,
	})
	if err != nil {
		return 0, err
	}

	nodes, dropped := buildImportedNodes(req.Outline, req.Drafts, createdIDOf)
	if len(nodes) == 0 {
		// 建了空卷又没法删，退一步：报错让调用方知道这次没成卷。
		_ = s.papers.DeletePaper(ctx, user, detail.ID)
		return 0, apperr.New(apperr.CodeUnprocessable, "大纲里没有可用的题目，未创建试卷")
	}

	if _, err := s.papers.ReplaceNodes(ctx, user, detail.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: detail.Revision,
		Nodes:            nodes,
	}); err != nil {
		// 同样要清掉半成品：留一份空壳卷比不建更糟。
		_ = s.papers.DeletePaper(ctx, user, detail.ID)
		return 0, err
	}
	if len(dropped) > 0 {
		slog.Warn("整卷导入时有题目未能进卷",
			"paper_id", detail.ID, "dropped", dropped)
	}
	return detail.ID, nil
}

// buildImportedNodes 把大纲翻译成组卷节点树。
//
// 返回值里的 dropped 是没能进卷的题目下标：它们要么入库失败了，
// 要么在大纲里被引用了两次。不静默丢弃——上面有调用方记日志。
func buildImportedNodes(
	outline []importing.Outline,
	drafts []importing.Draft,
	createdIDOf []uint,
) ([]domain.NodeInput, []int) {
	var (
		nodes   []domain.NodeInput
		dropped []int
	)
	// used 防止同一道题在大纲里被引用两次——那会在卷面上出现两道一样的题。
	used := make(map[int]bool, len(drafts))

	for _, item := range outline {
		switch item.Kind {
		case importing.OutlineHeading:
			if text := strings.TrimSpace(item.Text); text != "" {
				level := item.Level
				if level < 1 {
					level = 1
				}
				if level > 3 {
					// 更深的层级在导出的卷子里会缩得看不见，
					// 压到 3 至少还能看出是一段子标题。
					level = 3
				}
				nodes = append(nodes, headingNode(text, level))
			}

		case importing.OutlineRichText:
			if text := strings.TrimSpace(item.Text); text != "" {
				nodes = append(nodes, richTextNode(text))
			}

		case importing.OutlineQuestion:
			idx := item.DraftIndex
			if idx < 0 || idx >= len(createdIDOf) {
				// 大纲引用了不存在的草稿：跳过并记下来，不中断整卷。
				dropped = append(dropped, idx)
				continue
			}
			if used[idx] {
				dropped = append(dropped, idx)
				continue
			}
			questionID := createdIDOf[idx]
			if questionID == 0 {
				// 入库失败：宁可这道题不在卷上，也不要留一个失效引用。
				dropped = append(dropped, idx)
				continue
			}
			used[idx] = true
			nodes = append(nodes, questionNode(questionID))

		case importing.OutlinePageBreak:
			nodes = append(nodes, plainNode(model.NodeTypePageBreak, nil))

		case importing.OutlineAnswerSpace:
			nodes = append(nodes, answerSpaceNode(item.Lines, item.Style))

		case importing.OutlineDegraded:
			// 原样保留说明，让用户知道这块没还原。
			if note := strings.TrimSpace(item.Note); note != "" {
				nodes = append(nodes, richTextNode("（导入时未能还原：%s）", note))
			}
		}
	}

	return nodes, dropped
}

// headingNode 标题节点。
//
// level 同时写进 props 和富文本的 heading.attrs：前者是导出与编号分组
// 读的地方，后者是编辑器渲染的地方。两边都要有，否则会出现
// "界面上是一级标题、导出来是二级标题"这种对不上的情况。
func headingNode(text string, level int) domain.NodeInput {
	n := plainNode(model.NodeTypeHeading, docWithHeading(level, text))
	_ = setJSONField(&n.Props, "level", level)
	return n
}

func answerSpaceNode(lines int, style string) domain.NodeInput {
	n := plainNode(model.NodeTypeAnswerSpace, nil)
	if lines > 0 {
		_ = setJSONField(&n.Props, "lines", lines)
	}
	if style == "lined" {
		_ = setJSONField(&n.Props, "style", style)
	}
	return n
}

func questionNode(questionID uint) domain.NodeInput {
	n := plainNode(model.NodeTypeQuestion, nil)
	_ = setJSONContent(&n, domain.QuestionRef{QuestionID: questionID})
	return n
}

func richTextNode(text string, args ...any) domain.NodeInput {
	if len(args) > 0 {
		text = fmt.Sprintf(text, args...)
	}
	return plainNode(model.NodeTypeRichText, docWithText(text))
}

func plainNode(nodeType model.NodeType, content any) domain.NodeInput {
	kind, ok := model.KindOf(nodeType)
	if !ok {
		// 不认识的节点类型直接兜成块：组卷的树形结构只认这几种，
		// 与其让整卷建不出来，不如落成一个能被渲染的块。
		kind = model.NodeKindBlock
	}
	n := domain.NodeInput{
		ID:            uuid.NewString(),
		SchemaVersion: 1,
		NodeKind:      kind,
		NodeType:      nodeType,
		Props:         model.JSON("{}"),
		Content:       model.JSON("{}"),
	}
	if content != nil {
		_ = setJSONContent(&n, content)
	}
	return n
}

// docWithText 一段纯文本的富文本文档。
func docWithText(text string) any {
	return map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		},
	}
}

// docWithHeading 一个标题的富文本文档。
func docWithHeading(level int, text string) any {
	return map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "heading",
				"attrs":   map[string]any{"level": level},
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		},
	}
}

func setJSONProp(n *domain.NodeInput, key string, v any) error {
	return setJSONField(&n.Props, key, v)
}

// setJSONField 往一个 JSON 对象字段里塞一个键值。
func setJSONField(target *model.JSON, key string, v any) error {
	var m map[string]any
	if target != nil && !target.IsEmpty() {
		_ = target.Into(&m)
	}
	if m == nil {
		m = map[string]any{}
	}
	m[key] = v
	j, err := model.JSONOf(m)
	if err != nil {
		return err
	}
	*target = j
	return nil
}

func setJSONContent(n *domain.NodeInput, v any) error {
	j, err := model.JSONOf(v)
	if err != nil {
		return err
	}
	n.Content = j
	return nil
}
