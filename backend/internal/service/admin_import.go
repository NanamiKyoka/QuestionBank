package service

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/hash"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

const maxAdminImportRows = 5000

var hexColorRe = regexp.MustCompile(`^#(?:[0-9A-Fa-f]{3}|[0-9A-Fa-f]{6})$`)

type AdminImportService struct {
	repos     *Repos
	onIndexed func(subjectID uint, ids []uint)
}

func NewAdminImportService(repos *Repos) *AdminImportService {
	return &AdminImportService{repos: repos}
}

func (s *AdminImportService) SetIndexHook(fn func(subjectID uint, ids []uint)) {
	s.onIndexed = fn
}

func (s *AdminImportService) notifyIndexed(subjectID uint, ids []uint) bool {
	if s.onIndexed == nil || len(ids) == 0 {
		return false
	}
	s.onIndexed(subjectID, ids)
	return true
}

func (s *AdminImportService) UserTemplate() ([]byte, error) {
	return buildImportWorkbook("用户导入",
		[]string{"用户名", "姓名", "密码", "管理员"},
		[][]string{
			{"teacher01", "张三", "changeme123", ""},
			{"admin02", "李四", "changeme123", "是"},
		}, []float64{18, 16, 18, 12})
}

func (s *AdminImportService) TagTemplate() ([]byte, error) {
	return buildImportWorkbook("标签导入",
		[]string{"名称", "分类", "颜色"},
		[][]string{
			{"高考真题", "来源", "#4F81BD"},
			{"易错题", "", ""},
		}, []float64{22, 18, 16})
}

func (s *AdminImportService) KnowledgePointTemplate() ([]byte, error) {
	headers := []string{"一级目录", "二级目录", "三级目录", "四级目录", "五级目录", "六级目录", "七级目录", "八级目录"}
	rows := [][]string{
		{"代数", "方程", "一元一次方程"},
		{"代数", "方程", "一元二次方程", "解法", "公式法"},
		{"几何", "平面几何", "三角形"},
	}
	widths := make([]float64, len(headers))
	for i := range widths {
		widths[i] = 18
	}
	return buildImportWorkbook("知识点导入", headers, rows, widths)
}

func (s *AdminImportService) ImportUsers(ctx context.Context, actor *model.User, data []byte) (*schema.UserImportResult, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	headers, rows, err := readImportWorkbook(data)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("%s", err.Error())
	}
	cols, err := requireColumns(headers, "用户名", "姓名", "密码")
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("%s", err.Error())
	}

	result := &schema.UserImportResult{Errors: []schema.AdminImportRowError{}}
	seen := make(map[string]bool)
	for i, row := range rows {
		rowNo := i + 2
		username := importCell(row, cols, "用户名")
		fullName := importCell(row, cols, "姓名")
		password := importCell(row, cols, "密码")
		admin := truthyImportValue(importCell(row, cols, "管理员"))
		result.Total++

		switch {
		case username == "":
			result.Errors = append(result.Errors, schema.AdminImportRowError{Row: rowNo, Message: "用户名不能为空"})
			continue
		case fullName == "":
			result.Errors = append(result.Errors, schema.AdminImportRowError{Row: rowNo, Message: "姓名不能为空"})
			continue
		case password == "":
			result.Errors = append(result.Errors, schema.AdminImportRowError{Row: rowNo, Message: "密码不能为空"})
			continue
		case len([]rune(password)) < 6:
			result.Errors = append(result.Errors, schema.AdminImportRowError{Row: rowNo, Message: "密码长度不能少于 6 位"})
			continue
		case seen[username]:
			result.Errors = append(result.Errors, schema.AdminImportRowError{Row: rowNo, Message: fmt.Sprintf("用户名 %q 在文件中重复", username)})
			continue
		}

		seen[username] = true
		exists, err := s.repos.Users.ExistsUsername(ctx, username, 0)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if exists {
			result.Errors = append(result.Errors, schema.AdminImportRowError{Row: rowNo, Message: fmt.Sprintf("用户名 %q 已存在", username)})
			continue
		}
		passwordHash, err := hash.Password(password)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		user := &model.User{
			Username:     username,
			PasswordHash: passwordHash,
			Nickname:     fullName,
			IsSuperuser:  admin,
			IsActive:     true,
		}
		if err := s.repos.Users.Create(ctx, user); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		result.Created++
	}
	result.Failed = len(result.Errors)
	result.Status = importStatus(result.Created, result.Failed)
	return result, nil
}

func (s *AdminImportService) ImportTags(ctx context.Context, actor *model.User, subjectID uint, data []byte) (*schema.TagImportResult, error) {
	if err := authorize(actor, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}
	if _, err := s.repos.Subjects.Get(ctx, subjectID); err != nil {
		return nil, err
	}
	headers, rows, err := readImportWorkbook(data)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("%s", err.Error())
	}
	cols, err := requireColumns(headers, "名称")
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("%s", err.Error())
	}

	categories, err := s.repos.TagCategories.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	categoryID := make(map[string]uint, len(categories))
	for _, category := range categories {
		categoryID[category.Name] = category.ID
	}
	tags, err := s.repos.Tags.ListBySubject(ctx, subjectID, nil)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	existing := make(map[string]bool, len(tags))
	for _, tag := range tags {
		existing[tag.Name] = true
	}

	type tagRow struct {
		rowNo    int
		name     string
		category string
		color    string
	}
	parsed := make([]tagRow, 0, len(rows))
	seen := make(map[string]bool)
	errors := make([]schema.AdminImportRowError, 0)
	for i, row := range rows {
		rowNo := i + 2
		name := importCell(row, cols, "名称")
		category := importCell(row, cols, "分类")
		color := importCell(row, cols, "颜色")
		if name == "" {
			errors = append(errors, schema.AdminImportRowError{Row: rowNo, Message: "名称不能为空"})
			continue
		}
		if color != "" && !hexColorRe.MatchString(color) {
			errors = append(errors, schema.AdminImportRowError{Row: rowNo, Message: fmt.Sprintf("颜色 %q 格式不正确，需为 #RGB 或 #RRGGBB", color)})
			continue
		}
		if seen[name] || existing[name] {
			continue
		}
		seen[name] = true
		parsed = append(parsed, tagRow{rowNo: rowNo, name: name, category: category, color: color})
	}
	result := &schema.TagImportResult{Total: len(rows), Errors: errors}
	if len(errors) > 0 {
		result.Failed = len(errors)
		result.Status = "failed"
		return result, nil
	}
	for i := range parsed {
		if parsed[i].category == "" || categoryID[parsed[i].category] != 0 {
			continue
		}
		category := &model.TagCategory{SubjectID: subjectID, Name: parsed[i].category}
		if err := s.repos.TagCategories.Create(ctx, category); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		categoryID[parsed[i].category] = category.ID
	}
	for _, item := range parsed {
		tag := &model.Tag{SubjectID: subjectID, Name: item.name, Color: item.color, SortOrder: result.Created}
		if tag.Color == "" {
			tag.Color = "#cccccc"
		}
		if id := categoryID[item.category]; id != 0 {
			tag.CategoryID = &id
		}
		if err := s.repos.Tags.Create(ctx, tag); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		result.Created++
	}
	result.Skipped = len(rows) - len(parsed) - len(errors)
	result.Failed = len(errors)
	result.Status = importStatus(result.Created, result.Failed)
	return result, nil
}

func (s *AdminImportService) KnowledgePointPreflight(ctx context.Context, actor *model.User, subjectID uint) (*schema.KnowledgePointImportPreflight, error) {
	if err := authorize(actor, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}
	subject, err := s.repos.Subjects.Get(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	points, err := s.repos.KnowledgePoints.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	affectedQuestions, err := s.repos.KnowledgePoints.CountAffectedQuestions(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	return &schema.KnowledgePointImportPreflight{
		SubjectID:         subjectID,
		SubjectName:       subject.Name,
		ExistingCount:     int64(len(points)),
		AffectedQuestions: affectedQuestions,
	}, nil
}

func (s *AdminImportService) ImportKnowledgePoints(ctx context.Context, actor *model.User, subjectID uint, mode string, data []byte) (*schema.KnowledgePointImportResult, error) {
	if err := authorize(actor, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}
	if _, err := s.repos.Subjects.Get(ctx, subjectID); err != nil {
		return nil, err
	}
	if mode != "incremental" && mode != "rebuild" {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("导入模式只能是 incremental 或 rebuild")
	}
	headers, rows, err := readImportWorkbook(data)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("%s", err.Error())
	}
	cols, err := requireColumns(headers, "一级目录")
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "导入参数不正确").WithDetail("%s", err.Error())
	}
	levelNames := []string{"一级目录", "二级目录", "三级目录", "四级目录", "五级目录", "六级目录", "七级目录", "八级目录"}
	paths := make([][]string, 0, len(rows))
	errors := make([]schema.AdminImportRowError, 0)
	for i, row := range rows {
		rowNo := i + 2
		path := make([]string, 0, len(levelNames))
		for _, name := range levelNames {
			if value := importCell(row, cols, name); value != "" {
				path = append(path, value)
			} else {
				break
			}
		}
		if len(path) == 0 {
			errors = append(errors, schema.AdminImportRowError{Row: rowNo, Message: "一级目录不能为空"})
			continue
		}
		paths = append(paths, path)
	}
	result := &schema.KnowledgePointImportResult{Mode: mode, Total: len(rows), Errors: errors}
	if len(errors) > 0 {
		result.Failed = len(errors)
		result.Status = "failed"
		return result, nil
	}

	createdIDs := make([]uint, 0)
	createdRows := 0
	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		existing, err := s.repos.KnowledgePoints.ListBySubject(ctx, subjectID)
		if err != nil {
			return err
		}
		byParentName := make(map[string]*model.KnowledgePoint, len(existing))
		existingIDs := make([]uint, 0, len(existing))
		for i := range existing {
			item := &existing[i]
			existingIDs = append(existingIDs, item.ID)
			key := knowledgePointKey(item.ParentID, item.Name)
			byParentName[key] = item
		}
		if mode == "rebuild" {
			if err := s.repos.KnowledgePoints.UnlinkQuestions(ctx, existingIDs); err != nil {
				return err
			}
			if err := s.repos.KnowledgePoints.SoftDeleteByIDs(ctx, existingIDs); err != nil {
				return err
			}
			byParentName = make(map[string]*model.KnowledgePoint)
		}
		for _, path := range paths {
			var parent *model.KnowledgePoint
			rowCreated := false
			for _, name := range path {
				key := knowledgePointKey(parentID(parent), name)
				if item := byParentName[key]; item != nil {
					parent = item
					continue
				}
				level := 1
				parentPath := ""
				if parent != nil {
					level = parent.Level + 1
					parentPath = parent.Path
				}
				item := &model.KnowledgePoint{
					SubjectID: subjectID,
					ParentID:  parentID(parent),
					Name:      name,
					Level:     level,
					CreatedBy: actor.ID,
				}
				if err := s.repos.KnowledgePoints.Create(ctx, item); err != nil {
					return err
				}
				item.Path = fmt.Sprintf("%s%d/", parentPath, item.ID)
				if err := s.repos.KnowledgePoints.UpdateFields(ctx, item.ID, map[string]any{"path": item.Path}); err != nil {
					return err
				}
				byParentName[key] = item
				createdIDs = append(createdIDs, item.ID)
				rowCreated = true
				parent = item
			}
			if rowCreated {
				createdRows++
			}
		}
		return nil
	})
	if err != nil {
		return nil, apperr.From(err)
	}
	result.Created = len(createdIDs)
	if mode == "incremental" {
		result.Skipped = len(rows) - createdRows
	}
	result.VectorSynced = s.notifyIndexed(subjectID, createdIDs)
	result.Status = importStatus(result.Created, result.Failed)
	return result, nil
}

func parentID(parent *model.KnowledgePoint) *uint {
	if parent == nil {
		return nil
	}
	id := parent.ID
	return &id
}

func knowledgePointKey(parentID *uint, name string) string {
	if parentID == nil {
		return "0/" + name
	}
	return fmt.Sprintf("%d/%s", *parentID, name)
}

func importStatus(created, failed int) string {
	switch {
	case created > 0 && failed > 0:
		return "partial"
	case created > 0:
		return "success"
	default:
		return "failed"
	}
}

func truthyImportValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "是", "true", "1", "yes", "y", "admin", "管理员":
		return true
	default:
		return false
	}
}

func buildImportWorkbook(sheet string, headers []string, rows [][]string, widths []float64) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	defaultSheet := f.GetSheetName(0)
	if err := f.SetSheetName(defaultSheet, sheet); err != nil {
		return nil, err
	}
	if err := f.SetSheetRow(sheet, "A1", &headers); err != nil {
		return nil, err
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+2)
		if err != nil {
			return nil, err
		}
		if err := f.SetSheetRow(sheet, cell, &row); err != nil {
			return nil, err
		}
	}
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"4F81BD"}, Pattern: 1},
	})
	if err != nil {
		return nil, err
	}
	lastCol, err := excelize.ColumnNumberToName(len(headers))
	if err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(sheet, "A1", lastCol+"1", headerStyle); err != nil {
		return nil, err
	}
	for i, width := range widths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return nil, err
		}
		if err := f.SetColWidth(sheet, col, col, width); err != nil {
			return nil, err
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func readImportWorkbook(data []byte) ([]string, [][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("无法读取 Excel 文件：%w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil, fmt.Errorf("Excel 文件没有工作表")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, nil, fmt.Errorf("读取第一个工作表失败：%w", err)
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("Excel 文件没有表头")
	}
	headers := make([]string, len(rows[0]))
	for i, value := range rows[0] {
		headers[i] = strings.TrimSpace(value)
	}
	out := make([][]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if isEmptyImportRow(row) {
			continue
		}
		if len(out) >= maxAdminImportRows {
			return nil, nil, fmt.Errorf("单次导入最多支持 %d 行", maxAdminImportRows)
		}
		out = append(out, row)
	}
	return headers, out, nil
}

func requireColumns(headers []string, required ...string) (map[string]int, error) {
	cols := make(map[string]int, len(headers))
	for i, header := range headers {
		if header != "" {
			if _, exists := cols[header]; !exists {
				cols[header] = i
			}
		}
	}
	missing := make([]string, 0)
	for _, name := range required {
		if _, ok := cols[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("缺少必需的列：%s。请使用最新导入模板", strings.Join(missing, "、"))
	}
	return cols, nil
}

func importCell(row []string, cols map[string]int, name string) string {
	index, ok := cols[name]
	if !ok || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func isEmptyImportRow(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
