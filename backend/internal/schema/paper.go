package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ---- 目录 ----

type FolderCreateRequest struct {
	// ParentID 为空表示建在根下。
	ParentID *uint  `json:"parent_id"`
	Name     string `json:"name" binding:"required,min=1,max=128"`
	// Scope personal | shared，缺省为 personal。
	Scope model.PaperScope `json:"scope" binding:"omitempty,oneof=personal shared"`
}

type FolderUpdateRequest struct {
	Name *string `json:"name" binding:"omitempty,min=1,max=128"`
	// ParentID 传值表示移动；ClearParent 表示移动到根。
	ParentID    *uint `json:"parent_id"`
	ClearParent bool  `json:"clear_parent"`
}

type FolderResponse struct {
	ID        uint             `json:"id"`
	SubjectID uint             `json:"subject_id"`
	Scope     model.PaperScope `json:"scope"`
	OwnerID   *uint            `json:"owner_id"`
	ParentID  *uint            `json:"parent_id"`
	Name      string           `json:"name"`
	SortOrder int              `json:"sort_order"`

	PaperCount int `json:"paper_count"`

	// Children 目录树。目录数量少且天然分层，直接返回树比让客户端还原更方便。
	Children []FolderResponse `json:"children,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

func NewFolderResponse(f *model.PaperFolder) FolderResponse {
	return FolderResponse{
		ID:        f.ID,
		SubjectID: f.SubjectID,
		Scope:     f.Scope,
		OwnerID:   f.OwnerID,
		ParentID:  f.ParentID,
		Name:      f.Name,
		SortOrder: f.SortOrder,
		CreatedAt: f.CreatedAt,
	}
}

// ---- 试卷 ----

type PaperCreateRequest struct {
	Title       string `json:"title" binding:"required,min=1,max=200"`
	Description string `json:"description" binding:"omitempty,max=512"`

	Scope    model.PaperScope `json:"scope" binding:"omitempty,oneof=personal shared"`
	FolderID *uint            `json:"folder_id"`

	NumberingEnabled *bool `json:"numbering_enabled"`
	ScoringEnabled   *bool `json:"scoring_enabled"`
}

type PaperUpdateRequest struct {
	Title       *string            `json:"title" binding:"omitempty,min=1,max=200"`
	Description *string            `json:"description" binding:"omitempty,max=512"`
	Status      *model.PaperStatus `json:"status"`

	NumberingEnabled *bool `json:"numbering_enabled"`
	ScoringEnabled   *bool `json:"scoring_enabled"`

	// FolderID 传值表示移动；ClearFolder 表示移到根。
	FolderID    *uint `json:"folder_id"`
	ClearFolder bool  `json:"clear_folder"`

	// QuestionDisplay 题目字段显示开关。
	QuestionDisplay *model.QuestionDisplay `json:"question_display"`
}

// PaperBrief 列表项。
type PaperBrief struct {
	ID          uint              `json:"id"`
	SubjectID   uint              `json:"subject_id"`
	Scope       model.PaperScope  `json:"scope"`
	OwnerID     *uint             `json:"owner_id"`
	FolderID    *uint             `json:"folder_id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Status      model.PaperStatus `json:"status"`
	StatusLabel string            `json:"status_label"`

	QuestionCount int     `json:"question_count"`
	TotalScore    float64 `json:"total_score"`

	NumberingEnabled bool `json:"numbering_enabled"`
	ScoringEnabled   bool `json:"scoring_enabled"`

	// Revision 乐观锁版本号：客户端保存时原样回传。
	Revision         int64 `json:"revision"`
	FinalizedVersion int   `json:"finalized_version"`

	CreatedBy uint      `json:"created_by"`
	UpdatedBy uint      `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewPaperBrief(p *model.Paper) PaperBrief {
	return PaperBrief{
		ID:               p.ID,
		SubjectID:        p.SubjectID,
		Scope:            p.Scope,
		OwnerID:          p.OwnerID,
		FolderID:         p.FolderID,
		Title:            p.Title,
		Description:      p.Description,
		Status:           p.Status,
		StatusLabel:      paperStatusLabel(p.Status),
		TotalScore:       p.TotalScore,
		NumberingEnabled: p.NumberingEnabled,
		ScoringEnabled:   p.ScoringEnabled,
		Revision:         p.Revision,
		FinalizedVersion: p.FinalizedVersion,
		CreatedBy:        p.CreatedBy,
		UpdatedBy:        p.UpdatedBy,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func paperStatusLabel(s model.PaperStatus) string {
	switch s {
	case model.PaperStatusDraft:
		return "草稿"
	case model.PaperStatusFinalized:
		return "已定稿"
	case model.PaperStatusArchived:
		return "已归档"
	}
	return string(s)
}

// PaperNodeResponse 节点响应。
//
// 返回**扁平列表**（带 parent_id 与 position），由客户端自行还原成树。
// 统一扁平结构的好处是：提交与返回形状一致，客户端一套解析逻辑即可，
// 也避免了深层嵌套 JSON 在编辑器中反复重建的开销。
type PaperNodeResponse struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	// Position 同级内序号，由服务端分配。
	Position int `json:"position"`

	NodeKind model.NodeKind `json:"node_kind"`
	NodeType model.NodeType `json:"node_type"`

	Content model.JSON `json:"content"`
	Props   model.JSON `json:"props"`

	SchemaVersion int `json:"schema_version"`

	SourceQuestionNodeID *string `json:"source_question_node_id"`
	AnchorBeforeNodeID   *string `json:"anchor_before_node_id"`

	// QuestionID 题目块引用的题目 id（服务端解析自 Content）。
	QuestionID *uint `json:"question_id,omitempty"`
	// QuestionRevision 冻结那一刻题目的修订号。界面据此显示
	// 「可同步最新内容」，客户端不需要、也不应该提交这两个字段。
	QuestionRevision int64 `json:"question_revision,omitempty"`
}

func NewPaperNodeResponse(n *model.PaperNode) PaperNodeResponse {
	resp := PaperNodeResponse{
		ID:                   n.ID,
		ParentID:             n.ParentID,
		Position:             n.Position,
		NodeKind:             n.NodeKind,
		NodeType:             n.NodeType,
		Content:              n.Content,
		Props:                n.Props,
		SchemaVersion:        n.SchemaVersion,
		SourceQuestionNodeID: n.SourceQuestionNodeID,
		AnchorBeforeNodeID:   n.AnchorBeforeNodeID,
		QuestionID:           n.QuestionID,
	}
	if !n.QuestionContent.IsEmpty() {
		var fq struct {
			ContentRevision int64 `json:"content_revision"`
		}
		if err := n.QuestionContent.Into(&fq); err == nil {
			resp.QuestionRevision = fq.ContentRevision
		}
	}
	return resp
}

// PaperDetail 试卷详情。
type PaperDetail struct {
	PaperBrief

	QuestionDisplay model.QuestionDisplay `json:"question_display"`
	Nodes           []PaperNodeResponse   `json:"nodes"`

	// Questions 节点引用到的题目内容投影，客户端据此直接渲染，无需再逐题请求。
	Questions map[uint]PaperQuestionProjection `json:"questions"`

	Permissions PaperPermissions `json:"permissions"`
}

// PaperQuestionProjection 试卷中题目的内容投影。
type PaperQuestionProjection struct {
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
	StemText          string             `json:"stem_text"`

	// ContentRevision 题目当前的内容修订号，用于与节点上记录的对比。
	ContentRevision int64 `json:"content_revision"`
	// Stale 为真表示引用的题目已被改动，界面提示"同步最新内容"。
	Stale bool `json:"stale"`

	// Visibility / CreatedBy 记的是**冻结那一刻**的可见性。
	// 定稿快照会连同它们一起存下来，于是导出历史版本时能重新判断
	// "题是不是后来变私有了"——否则定稿之后再把题改成私有，
	// 旧版本仍然会把内容原样导出来。
	Visibility model.Visibility `json:"visibility,omitempty"`
	CreatedBy  uint             `json:"created_by,omitempty"`
}

type PaperPermissions struct {
	CanEdit     bool `json:"can_edit"`
	CanDelete   bool `json:"can_delete"`
	CanFinalize bool `json:"can_finalize"`
}

// ---- 节点树替换 ----

// ReplaceNodesRequest 整树替换。
//
// 采用"整树覆盖"而不是增量补丁：排版操作天然是整篇重排（插入一题会让后面
// 所有题号变化），增量补丁反而更难保证一致性。配合 revision 乐观锁即可安全。
type ReplaceNodesRequest struct {
	// ExpectedRevision 客户端最后看到的版本号；与服务端不一致则返回 409。
	ExpectedRevision int64 `json:"expected_revision" binding:"required"`
	// BatchID 同一次编辑会话的标识，用于操作时间线去重。
	BatchID string `json:"batch_id" binding:"omitempty,max=64"`

	Nodes []domain.NodeInput `json:"nodes"`
}

// ReplaceNodesResponse 保存结果。
type ReplaceNodesResponse struct {
	Revision int64               `json:"revision"`
	Nodes    []PaperNodeResponse `json:"nodes"`
}

// QuestionRevisionsResponse 题目时效性检查结果。
type QuestionRevisionsResponse struct {
	// Stale 过期的题目：节点记录的修订号与题目当前修订号不一致。
	Stale []uint `json:"stale"`
	// Missing 已被删除、无法再引用的题目。
	Missing []uint `json:"missing"`
}

// SyncQuestionNodesResponse 同步结果。
type SyncQuestionNodesResponse struct {
	Revision int64 `json:"revision"`
	// Synced 本次刷新了修订号的题目。
	Synced []uint `json:"synced"`
}

// ---- 版本 ----

type FinalizeRequest struct {
	Note string `json:"note" binding:"omitempty,max=512"`
}

type VersionResponse struct {
	ID            uint      `json:"id"`
	PaperID       uint      `json:"paper_id"`
	VersionNo     int       `json:"version_no"`
	Title         string    `json:"title"`
	Note          string    `json:"note"`
	SchemaVersion int       `json:"schema_version"`
	CreatedBy     uint      `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`

	// QuestionCount 快照中的题目数，列表展示用。
	QuestionCount int `json:"question_count"`
}

// VersionDetailResponse 版本详情，含冻结的内容快照。
type VersionDetailResponse struct {
	VersionResponse
	Snapshot model.JSON `json:"snapshot"`
}

// ---- 操作时间线 ----

type EventResponse struct {
	ID        uint       `json:"id"`
	EventType string     `json:"event_type"`
	Summary   string     `json:"summary"`
	ActorID   uint       `json:"actor_id"`
	Actor     string     `json:"actor"`
	Payload   model.JSON `json:"payload"`
	CreatedAt time.Time  `json:"created_at"`
}
