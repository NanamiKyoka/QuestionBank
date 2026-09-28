package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// KnowledgePointCreateRequest 新建知识点。
type KnowledgePointCreateRequest struct {
	// ParentID 为空表示建为根节点。
	ParentID    *uint  `json:"parent_id"`
	Name        string `json:"name" binding:"required,min=1,max=128"`
	Description string `json:"description" binding:"omitempty,max=512"`
	SortOrder   *int   `json:"sort_order"`
}

// KnowledgePointUpdateRequest 更新知识点。
//
// ParentID 用「是否出现」而非「是否为零」判断：允许把节点提升为根，
// 因此不能简单地用 0 表示"不修改"。
type KnowledgePointUpdateRequest struct {
	Name        *string `json:"name" binding:"omitempty,min=1,max=128"`
	Description *string `json:"description" binding:"omitempty,max=512"`
	SortOrder   *int    `json:"sort_order"`
}

// KnowledgePointMoveRequest 移动知识点到新的父节点。
// ParentID 为 null 表示提升为根节点。
type KnowledgePointMoveRequest struct {
	ParentID *uint `json:"parent_id"`
}

type KnowledgePointResponse struct {
	ID          uint   `json:"id"`
	SubjectID   uint   `json:"subject_id"`
	ParentID    *uint  `json:"parent_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Level       int    `json:"level"`
	SortOrder   int    `json:"sort_order"`

	// QuestionCount 关联题目数，列表页直接显示，避免前端逐个查询。
	QuestionCount int64 `json:"question_count"`

	Children []KnowledgePointResponse `json:"children,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// NewKnowledgePointResponse 构造单节点响应（不含子节点）。
func NewKnowledgePointResponse(k *model.KnowledgePoint) KnowledgePointResponse {
	return KnowledgePointResponse{
		ID:          k.ID,
		SubjectID:   k.SubjectID,
		ParentID:    k.ParentID,
		Name:        k.Name,
		Description: k.Description,
		Path:        k.Path,
		Level:       k.Level,
		SortOrder:   k.SortOrder,
		CreatedAt:   k.CreatedAt,
	}
}

// BuildKnowledgePointTree 把扁平列表组装成树。
//
// 之所以在服务端建树而不让前端做：一是前端少一次递归逻辑，
// 二是排序规则（先 sort_order 后 id）只需要在一处维护。
func BuildKnowledgePointTree(list []model.KnowledgePoint) []KnowledgePointResponse {
	nodes := make(map[uint]*KnowledgePointResponse, len(list))
	for i := range list {
		n := NewKnowledgePointResponse(&list[i])
		nodes[n.ID] = &n
	}

	roots := make([]KnowledgePointResponse, 0, 8)
	for i := range list {
		cur := nodes[list[i].ID]
		if list[i].ParentID != nil {
			if parent, ok := nodes[*list[i].ParentID]; ok {
				parent.Children = append(parent.Children, *cur)
				continue
			}
		}
		roots = append(roots, *cur)
	}

	// 自底向上重挂子节点，保证嵌套层级也带上完整的 children。
	var attach func(n *KnowledgePointResponse)
	attach = func(n *KnowledgePointResponse) {
		for i := range n.Children {
			if child, ok := nodes[n.Children[i].ID]; ok {
				n.Children[i] = *child
			}
			attach(&n.Children[i])
		}
	}
	for i := range roots {
		if node, ok := nodes[roots[i].ID]; ok {
			roots[i] = *node
		}
		attach(&roots[i])
	}
	return roots
}
