package service

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/export"
	"github.com/NanamiKyoka/questionmind/internal/infra/docx"
	"github.com/NanamiKyoka/questionmind/internal/infra/latex"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// ExportFormat 导出格式。
type ExportFormat string

const (
	FormatDocx  ExportFormat = "docx"
	FormatLaTeX ExportFormat = "latex"
)

// Valid 校验格式。
func (f ExportFormat) Valid() bool {
	return f == FormatDocx || f == FormatLaTeX
}

// Extension 返回文件扩展名（LaTeX 导出实际是包含图片的压缩包）。
func (f ExportFormat) Extension() string {
	if f == FormatLaTeX {
		return "zip"
	}
	return "docx"
}

// ExportResult 导出结果。
type ExportResult struct {
	Data        []byte
	Filename    string
	ContentType string
}

// ExportService 组卷导出用例。
type ExportService struct {
	repos  *Repos
	loader export.ImageLoader
}

// NewExportService 构造。loader 为 nil 时导出会跳过图片。
func NewExportService(repos *Repos, loader export.ImageLoader) *ExportService {
	return &ExportService{repos: repos, loader: loader}
}

// ExportPaper 导出当前试卷。
func (s *ExportService) ExportPaper(ctx context.Context, user *model.User, paperID uint, format ExportFormat) (*ExportResult, error) {
	if !format.Valid() {
		return nil, apperr.New(apperr.CodeInvalidParam, "不支持的导出格式").
			WithDetail("format=%s", format)
	}

	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}
	if paper.Scope == model.PaperScopePersonal &&
		(paper.OwnerID == nil || *paper.OwnerID != user.ID) && !user.IsSuperuser {
		return nil, apperr.ErrNotFound
	}

	nodes, err := s.repos.Papers.Nodes(ctx, paperID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if len(nodes) == 0 {
		return nil, apperr.New(apperr.CodeUnprocessable, "试卷还没有内容，无法导出")
	}

	views, err := s.questionViews(ctx, nodes)
	if err != nil {
		return nil, err
	}
	subjectName, err := s.subjectName(ctx, paper.SubjectID)
	if err != nil {
		return nil, err
	}

	var display model.QuestionDisplay
	if !paper.QuestionDisplay.IsEmpty() {
		_ = paper.QuestionDisplay.Into(&display)
	}

	doc := export.Assemble(export.Input{
		Title:            paper.Title,
		SubjectName:      subjectName,
		NumberingEnabled: paper.NumberingEnabled,
		ScoringEnabled:   paper.ScoringEnabled,
		Display:          display,
		Nodes:            nodes,
		Questions:        views,
		TotalScore:       paper.TotalScore,
	}, s.loader)

	return s.render(doc, paper.Title, format)
}

// ExportVersion 导出一个已定稿的历史版本。
//
// 关键点：完全基于快照渲染，不查当前的题库内容。
// 这样即使题目后来被改动或删除，导出的历史版本仍与当初定稿时一致——
// 这正是"定稿"这个动作的意义。
func (s *ExportService) ExportVersion(ctx context.Context, user *model.User, paperID uint, versionNo int, format ExportFormat) (*ExportResult, error) {
	if !format.Valid() {
		return nil, apperr.New(apperr.CodeInvalidParam, "不支持的导出格式")
	}

	paper, err := s.repos.Papers.Get(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if err := authorizeRead(user, paper.SubjectID); err != nil {
		return nil, err
	}

	version, err := s.repos.Papers.GetVersion(ctx, paperID, versionNo)
	if err != nil {
		return nil, err
	}

	var snap paperSnapshot
	if err := version.Snapshot.Into(&snap); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	views := make(map[uint]export.QuestionView, len(snap.Questions))
	for id, q := range snap.Questions {
		views[id] = export.QuestionView{
			ID:        q.ID,
			Type:      q.QuestionType,
			TypeLabel: q.QuestionTypeLabel,
			Stem:      q.Stem,
			Options:   q.Options,
			Answer:    q.Answer,
			Analysis:  q.Analysis,
			Solution:  q.Solution,
			Summary:   q.Summary,
		}
	}

	subjectName, _ := s.subjectName(ctx, paper.SubjectID)
	title := snap.Title
	if title == "" {
		title = version.Title
	}

	doc := export.Assemble(export.Input{
		Title:            title,
		SubjectName:      subjectName,
		NumberingEnabled: snap.NumberingEnabled,
		ScoringEnabled:   snap.ScoringEnabled,
		Display:          snap.QuestionDisplay,
		Nodes:            snap.Nodes,
		Questions:        views,
		TotalScore:       snap.TotalScore,
	}, s.loader)

	name := fmt.Sprintf("%s-v%d", title, versionNo)
	return s.render(doc, name, format)
}

// render 按格式渲染。
func (s *ExportService) render(doc *export.Document, baseName string, format ExportFormat) (*ExportResult, error) {
	safe := safeFilename(baseName)

	switch format {
	case FormatDocx:
		data, err := docx.Write(doc)
		if err != nil {
			return nil, apperr.New(apperr.CodeExportFailed, "生成 Word 文档失败").WithCause(err)
		}
		return &ExportResult{
			Data:        data,
			Filename:    safe + ".docx",
			ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		}, nil

	case FormatLaTeX:
		res, err := latex.Write(doc)
		if err != nil {
			return nil, apperr.New(apperr.CodeExportFailed, "生成 LaTeX 源码失败").WithCause(err)
		}
		// 图片需要与 .tex 一起打包，否则拿到源码也编译不出来。
		zipped, err := zipLaTeX(safe, res)
		if err != nil {
			return nil, apperr.New(apperr.CodeExportFailed, "打包 LaTeX 工程失败").WithCause(err)
		}
		return &ExportResult{
			Data:        zipped,
			Filename:    safe + "-latex.zip",
			ContentType: "application/zip",
		}, nil
	}

	return nil, apperr.New(apperr.CodeInvalidParam, "不支持的导出格式")
}

// zipLaTeX 把 .tex 与图片打成一个压缩包。
func zipLaTeX(name string, res *latex.Result) ([]byte, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	f, err := zw.Create(name + ".tex")
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(res.TeX); err != nil {
		return nil, err
	}

	for path, data := range res.Images {
		f, err := zw.Create(path)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// questionViews 装载节点引用到的题目内容。
func (s *ExportService) questionViews(ctx context.Context, nodes []model.PaperNode) (map[uint]export.QuestionView, error) {
	out := map[uint]export.QuestionView{}

	ids := domain.CollectQuestionIDs(nodes)
	if len(ids) == 0 {
		return out, nil
	}

	list, err := s.repos.Questions.ListByIDs(ctx, ids)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	for i := range list {
		q := &list[i]
		var options []model.Option
		if !q.Options.IsEmpty() {
			_ = q.Options.Into(&options)
		}
		var answer model.AnswerSpec
		if !q.Answer.IsEmpty() {
			_ = q.Answer.Into(&answer)
		}
		out[q.ID] = export.QuestionView{
			ID:        q.ID,
			Type:      q.QuestionType,
			TypeLabel: q.QuestionType.Label(),
			Stem:      q.Stem,
			Options:   options,
			Answer:    answer,
			Analysis:  q.Analysis,
			Solution:  q.Solution,
			Summary:   q.Summary,
		}
	}
	return out, nil
}

func (s *ExportService) subjectName(ctx context.Context, subjectID uint) (string, error) {
	subject, err := s.repos.Subjects.Get(ctx, subjectID)
	if err != nil {
		// 学科名只用于文档元信息，取不到不影响导出。
		return "", nil
	}
	return subject.Name, nil
}

// paperSnapshot 定稿快照的结构，与 FinalizeVersion 写入的形状对应。
type paperSnapshot struct {
	Title            string                    `json:"title"`
	Nodes            []model.PaperNode         `json:"nodes"`
	Questions        map[uint]snapshotQuestion `json:"questions"`
	NumberingEnabled bool                      `json:"numbering_enabled"`
	ScoringEnabled   bool                      `json:"scoring_enabled"`
	QuestionDisplay  model.QuestionDisplay     `json:"question_display"`
	TotalScore       float64                   `json:"total_score"`
	SchemaVersion    int                       `json:"schema_version"`
}

type snapshotQuestion struct {
	ID                uint               `json:"id"`
	QuestionType      model.QuestionType `json:"question_type"`
	QuestionTypeLabel string             `json:"question_type_label"`
	Difficulty        int                `json:"difficulty"`
	Stem              model.JSON         `json:"stem"`
	Options           []model.Option     `json:"options"`
	Answer            model.AnswerSpec   `json:"answer"`
	Analysis          model.JSON         `json:"analysis"`
	Solution          model.JSON         `json:"solution"`
	Summary           model.JSON         `json:"summary"`
}

// safeFilename 生成安全的文件名。
// 去掉路径分隔符与 Windows 保留字符，避免下载时被解释成路径。
func safeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "paper"
	}
	var sb strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r', '\t':
			sb.WriteRune('_')
		default:
			sb.WriteRune(r)
		}
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		out = "paper"
	}
	if len([]rune(out)) > 80 {
		out = string([]rune(out)[:80])
	}
	return out
}
