package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// KnowledgePointService 知识点树用例。
type KnowledgePointService struct {
	repos *Repos
}

func NewKnowledgePointService(repos *Repos) *KnowledgePointService {
	return &KnowledgePointService{repos: repos}
}

// Tree 返回学科的完整知识点树，并附带各节点的关联题目数。
func (s *KnowledgePointService) Tree(ctx context.Context, user *model.User, subjectID uint) ([]schema.KnowledgePointResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	list, err := s.repos.KnowledgePoints.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	counts, err := s.repos.KnowledgePoints.CountQuestionsByKP(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	tree := schema.BuildKnowledgePointTree(list)
	attachCounts(tree, counts)
	return tree, nil
}

func attachCounts(nodes []schema.KnowledgePointResponse, counts map[uint]int64) {
	for i := range nodes {
		nodes[i].QuestionCount = counts[nodes[i].ID]
		attachCounts(nodes[i].Children, counts)
	}
}

// Create 新建知识点。
//
// path/level 需要在拿到自增 ID 之后才能确定，因此放在事务里两步完成。
func (s *KnowledgePointService) Create(ctx context.Context, user *model.User, subjectID uint, req *schema.KnowledgePointCreateRequest) (*schema.KnowledgePointResponse, error) {
	if err := authorize(user, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}

	var created *model.KnowledgePoint

	err := s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		level := 1
		parentPath := ""

		if req.ParentID != nil {
			parent, err := s.repos.KnowledgePoints.Get(ctx, *req.ParentID)
			if err != nil {
				return err
			}
			if parent.SubjectID != subjectID {
				return apperr.New(apperr.CodeCrossSubject, "父知识点不属于该学科")
			}
			level = parent.Level + 1
			parentPath = parent.Path
		}

		dup, err := s.repos.KnowledgePoints.ExistsName(ctx, subjectID, req.ParentID, req.Name, 0)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return apperr.New(apperr.CodeDuplicateName, "同级下已存在同名知识点")
		}

		kp := &model.KnowledgePoint{
			SubjectID:   subjectID,
			ParentID:    req.ParentID,
			Name:        req.Name,
			Description: req.Description,
			Level:       level,
			CreatedBy:   user.ID,
		}
		if req.SortOrder != nil {
			kp.SortOrder = *req.SortOrder
		}
		if err := s.repos.KnowledgePoints.Create(ctx, kp); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}

		path := fmt.Sprintf("%s%d/", parentPath, kp.ID)
		kp.Path = path
		if err := s.repos.KnowledgePoints.UpdateFields(ctx, kp.ID, map[string]any{"path": path}); err != nil {
			return apperr.From(err)
		}
		created = kp
		return nil
	})
	if err != nil {
		return nil, err
	}

	resp := schema.NewKnowledgePointResponse(created)
	return &resp, nil
}

// Update 修改知识点自身属性（不含移动）。
func (s *KnowledgePointService) Update(ctx context.Context, user *model.User, id uint, req *schema.KnowledgePointUpdateRequest) (*schema.KnowledgePointResponse, error) {
	kp, err := s.repos.KnowledgePoints.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermManageSubject, kp.SubjectID); err != nil {
		return nil, err
	}

	fields := make(map[string]any, 3)
	if req.Name != nil {
		dup, err := s.repos.KnowledgePoints.ExistsName(ctx, kp.SubjectID, kp.ParentID, *req.Name, id)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return nil, apperr.New(apperr.CodeDuplicateName, "同级下已存在同名知识点")
		}
		fields["name"] = *req.Name
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.SortOrder != nil {
		fields["sort_order"] = *req.SortOrder
	}

	if err := s.repos.KnowledgePoints.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}

	updated, err := s.repos.KnowledgePoints.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewKnowledgePointResponse(updated)
	return &resp, nil
}

// Move 把知识点挂到新的父节点下，整棵子树的 path 与 level 同步重写。
func (s *KnowledgePointService) Move(ctx context.Context, user *model.User, id uint, req *schema.KnowledgePointMoveRequest) (*schema.KnowledgePointResponse, error) {
	node, err := s.repos.KnowledgePoints.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermManageSubject, node.SubjectID); err != nil {
		return nil, err
	}
	if req.ParentID != nil && *req.ParentID == id {
		return nil, apperr.New(apperr.CodeInvalidParam, "不能把知识点移动到自己下面")
	}

	newLevel := 1
	newParentPath := ""

	if req.ParentID != nil {
		parent, err := s.repos.KnowledgePoints.Get(ctx, *req.ParentID)
		if err != nil {
			return nil, err
		}
		if parent.SubjectID != node.SubjectID {
			return nil, apperr.New(apperr.CodeCrossSubject, "父知识点不属于同一学科")
		}
		// 关键校验：新父节点不能落在自己的子树里，否则会形成环，
		// 树的遍历与物化路径都会彻底失效。
		if strings.HasPrefix(parent.Path, node.Path) {
			return nil, apperr.New(apperr.CodeInvalidParam, "不能把知识点移动到它自己的子节点下")
		}
		newLevel = parent.Level + 1
		newParentPath = parent.Path
	}

	dup, err := s.repos.KnowledgePoints.ExistsName(ctx, node.SubjectID, req.ParentID, node.Name, id)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if dup {
		return nil, apperr.New(apperr.CodeDuplicateName, "目标位置下已存在同名知识点")
	}

	newPath := fmt.Sprintf("%s%d/", newParentPath, node.ID)
	levelDelta := newLevel - node.Level

	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.KnowledgePoints.RewriteSubtreePath(ctx, node.SubjectID, node.Path, newPath, levelDelta); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.KnowledgePoints.UpdateFields(ctx, node.ID, map[string]any{"parent_id": req.ParentID})
	})
	if err != nil {
		return nil, err
	}

	updated, err := s.repos.KnowledgePoints.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewKnowledgePointResponse(updated)
	return &resp, nil
}

// Delete 删除知识点及其整棵子树，同时解除与题目的关联。
func (s *KnowledgePointService) Delete(ctx context.Context, user *model.User, id uint) error {
	node, err := s.repos.KnowledgePoints.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := authorize(user, model.PermManageSubject, node.SubjectID); err != nil {
		return err
	}

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		subtree, err := s.repos.KnowledgePoints.Subtree(ctx, node.SubjectID, node.Path)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		ids := make([]uint, 0, len(subtree))
		for i := range subtree {
			ids = append(ids, subtree[i].ID)
		}

		if err := s.repos.KnowledgePoints.UnlinkQuestions(ctx, ids); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if err := s.repos.KnowledgePoints.SoftDeleteByIDs(ctx, ids); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return nil
	})
}
