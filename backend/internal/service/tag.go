package service

import (
	"context"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// TagService 标签与标签分类用例。
type TagService struct {
	repos *Repos
}

func NewTagService(repos *Repos) *TagService { return &TagService{repos: repos} }

// ---- 分类 ----

func (s *TagService) ListCategories(ctx context.Context, user *model.User, subjectID uint) ([]schema.TagCategoryResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	list, err := s.repos.TagCategories.ListBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	counts, err := s.repos.TagCategories.CountTags(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.TagCategoryResponse, 0, len(list))
	for i := range list {
		item := schema.NewTagCategoryResponse(&list[i])
		item.TagCount = counts[item.ID]
		out = append(out, item)
	}
	return out, nil
}

func (s *TagService) CreateCategory(ctx context.Context, user *model.User, subjectID uint, req *schema.TagCategoryCreateRequest) (*schema.TagCategoryResponse, error) {
	if err := authorize(user, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}

	dup, err := s.repos.TagCategories.ExistsName(ctx, subjectID, req.Name, 0)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if dup {
		return nil, apperr.New(apperr.CodeDuplicateName, "已存在同名标签分类")
	}

	cat := &model.TagCategory{SubjectID: subjectID, Name: req.Name}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}
	if err := s.repos.TagCategories.Create(ctx, cat); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	resp := schema.NewTagCategoryResponse(cat)
	return &resp, nil
}

func (s *TagService) UpdateCategory(ctx context.Context, user *model.User, id uint, req *schema.TagCategoryUpdateRequest) (*schema.TagCategoryResponse, error) {
	cat, err := s.repos.TagCategories.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermManageSubject, cat.SubjectID); err != nil {
		return nil, err
	}

	fields := make(map[string]any, 2)
	if req.Name != nil {
		dup, err := s.repos.TagCategories.ExistsName(ctx, cat.SubjectID, *req.Name, id)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return nil, apperr.New(apperr.CodeDuplicateName, "已存在同名标签分类")
		}
		fields["name"] = *req.Name
	}
	if req.SortOrder != nil {
		fields["sort_order"] = *req.SortOrder
	}
	if err := s.repos.TagCategories.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}

	updated, err := s.repos.TagCategories.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewTagCategoryResponse(updated)
	return &resp, nil
}

// DeleteCategory 删除分类，其下标签自动变为未分类。
func (s *TagService) DeleteCategory(ctx context.Context, user *model.User, id uint) error {
	cat, err := s.repos.TagCategories.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := authorize(user, model.PermManageSubject, cat.SubjectID); err != nil {
		return err
	}

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.TagCategories.ClearCategory(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if err := s.repos.TagCategories.SoftDelete(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return nil
	})
}

// ---- 标签 ----

func (s *TagService) List(ctx context.Context, user *model.User, subjectID uint, categoryID *uint) ([]schema.TagResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	list, err := s.repos.Tags.ListBySubject(ctx, subjectID, categoryID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	counts, err := s.repos.Tags.CountQuestionsByTag(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.TagResponse, 0, len(list))
	for i := range list {
		item := schema.NewTagResponse(&list[i])
		item.QuestionCount = counts[item.ID]
		out = append(out, item)
	}
	return out, nil
}

func (s *TagService) Create(ctx context.Context, user *model.User, subjectID uint, req *schema.TagCreateRequest) (*schema.TagResponse, error) {
	if err := authorize(user, model.PermManageSubject, subjectID); err != nil {
		return nil, err
	}

	dup, err := s.repos.Tags.ExistsName(ctx, subjectID, req.Name, 0)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if dup {
		return nil, apperr.New(apperr.CodeDuplicateName, "已存在同名标签")
	}
	if req.CategoryID != nil {
		if err := s.checkCategory(ctx, subjectID, *req.CategoryID); err != nil {
			return nil, err
		}
	}

	tag := &model.Tag{
		SubjectID:  subjectID,
		CategoryID: req.CategoryID,
		Name:       req.Name,
		Color:      req.Color,
	}
	if req.SortOrder != nil {
		tag.SortOrder = *req.SortOrder
	}
	if err := s.repos.Tags.Create(ctx, tag); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	created, err := s.repos.Tags.Get(ctx, tag.ID)
	if err != nil {
		return nil, err
	}
	resp := schema.NewTagResponse(created)
	return &resp, nil
}

func (s *TagService) Update(ctx context.Context, user *model.User, id uint, req *schema.TagUpdateRequest) (*schema.TagResponse, error) {
	tag, err := s.repos.Tags.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermManageSubject, tag.SubjectID); err != nil {
		return nil, err
	}

	fields := make(map[string]any, 4)
	if req.Name != nil {
		dup, err := s.repos.Tags.ExistsName(ctx, tag.SubjectID, *req.Name, id)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return nil, apperr.New(apperr.CodeDuplicateName, "已存在同名标签")
		}
		fields["name"] = *req.Name
	}
	if req.Color != nil {
		fields["color"] = *req.Color
	}
	if req.SortOrder != nil {
		fields["sort_order"] = *req.SortOrder
	}
	switch {
	case req.ClearCategory:
		fields["category_id"] = nil
	case req.CategoryID != nil:
		if err := s.checkCategory(ctx, tag.SubjectID, *req.CategoryID); err != nil {
			return nil, err
		}
		fields["category_id"] = *req.CategoryID
	}

	if err := s.repos.Tags.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}

	updated, err := s.repos.Tags.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewTagResponse(updated)
	return &resp, nil
}

// Delete 删除标签，并解除它与题目的关联。
func (s *TagService) Delete(ctx context.Context, user *model.User, id uint) error {
	tag, err := s.repos.Tags.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := authorize(user, model.PermManageSubject, tag.SubjectID); err != nil {
		return err
	}

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.repos.Tags.UnlinkQuestions(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if err := s.repos.Tags.SoftDelete(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return nil
	})
}

// checkCategory 校验分类存在且属于同一学科，防止跨学科挂标签。
func (s *TagService) checkCategory(ctx context.Context, subjectID, categoryID uint) error {
	cat, err := s.repos.TagCategories.Get(ctx, categoryID)
	if err != nil {
		return err
	}
	if cat.SubjectID != subjectID {
		return apperr.New(apperr.CodeCrossSubject, "标签分类不属于该学科")
	}
	return nil
}
