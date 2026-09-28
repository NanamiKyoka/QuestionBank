package service

import (
	"context"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// SubjectService 学科与成员用例。
type SubjectService struct {
	repos *Repos
}

func NewSubjectService(repos *Repos) *SubjectService { return &SubjectService{repos: repos} }

// List 返回当前用户可见的学科，并带上其在该学科的角色。
func (s *SubjectService) List(ctx context.Context, user *model.User) ([]schema.SubjectResponse, error) {
	ids, unrestricted := user.SubjectIDs()

	var filter map[uint]bool
	if !unrestricted {
		filter = ids
	}

	list, err := s.repos.Subjects.List(ctx, filter)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.SubjectResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewSubjectResponse(&list[i], s.roleOf(user, list[i].ID)))
	}
	return out, nil
}

// roleOf 计算用户在某学科的角色。管理员按学科负责人处理。
func (s *SubjectService) roleOf(user *model.User, subjectID uint) model.SubjectRole {
	if user.IsSuperuser {
		return model.SubjectRoleManager
	}
	if role, ok := user.RoleInSubject(subjectID); ok {
		return role
	}
	return ""
}

// Get 读取单个学科。
func (s *SubjectService) Get(ctx context.Context, user *model.User, id uint) (*schema.SubjectResponse, error) {
	if err := authorizeRead(user, id); err != nil {
		return nil, err
	}
	sub, err := s.repos.Subjects.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewSubjectResponse(sub, s.roleOf(user, id))
	return &resp, nil
}

// Create 新建学科，并把创建者设为该学科的负责人。
//
// 只有全局管理员能建学科：学科是系统的顶层边界，一旦放开给普通用户，
// 就会出现大量无人管理的空学科。
func (s *SubjectService) Create(ctx context.Context, user *model.User, req *schema.SubjectCreateRequest) (*schema.SubjectResponse, error) {
	if err := authorizeSuperuser(user); err != nil {
		return nil, err
	}

	if err := s.checkUnique(ctx, req.Name, req.Code, 0); err != nil {
		return nil, err
	}

	var created *model.Subject
	err := s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		sub := &model.Subject{
			Name:            req.Name,
			Code:            req.Code,
			Description:     req.Description,
			ReviewThreshold: 1,
			CreatedBy:       user.ID,
		}
		if req.ReviewThreshold != nil {
			sub.ReviewThreshold = *req.ReviewThreshold
		}
		if req.SortOrder != nil {
			sub.SortOrder = *req.SortOrder
		}

		if err := s.repos.Subjects.Create(ctx, sub); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		// 创建者自动成为负责人，否则新学科没人能管理。
		if err := s.repos.Subjects.AddMember(ctx, sub.ID, user.ID, model.SubjectRoleManager); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		created = sub
		return nil
	})
	if err != nil {
		return nil, err
	}

	resp := schema.NewSubjectResponse(created, model.SubjectRoleManager)
	return &resp, nil
}

// Update 修改学科配置。
func (s *SubjectService) Update(ctx context.Context, user *model.User, id uint, req *schema.SubjectUpdateRequest) (*schema.SubjectResponse, error) {
	if err := authorize(user, model.PermManageSubject, id); err != nil {
		return nil, err
	}

	name, code := "", ""
	if req.Name != nil {
		name = *req.Name
	}
	if req.Code != nil {
		code = *req.Code
	}
	if name != "" || code != "" {
		if err := s.checkUnique(ctx, name, code, id); err != nil {
			return nil, err
		}
	}

	if fields := req.Fields(); len(fields) > 0 {
		if err := s.repos.Subjects.UpdateFields(ctx, id, fields); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}
	return s.Get(ctx, user, id)
}

// Delete 删除学科。存在从属数据时拒绝，避免误删一整门学科的题库。
func (s *SubjectService) Delete(ctx context.Context, user *model.User, id uint) error {
	if err := authorizeSuperuser(user); err != nil {
		return err
	}
	if _, err := s.repos.Subjects.Get(ctx, id); err != nil {
		return err
	}

	dep, err := s.repos.Subjects.CountDependents(ctx, id)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if dep.Questions > 0 || dep.KnowledgePoints > 0 || dep.Papers > 0 {
		return apperr.New(apperr.CodeUnprocessable,
			"该学科下仍有数据，无法删除").
			WithDetail("题目 %d、知识点 %d、试卷 %d", dep.Questions, dep.KnowledgePoints, dep.Papers)
	}

	if err := s.repos.Subjects.SoftDelete(ctx, id); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

func (s *SubjectService) checkUnique(ctx context.Context, name, code string, excludeID uint) error {
	if name != "" {
		dup, err := s.repos.Subjects.ExistsName(ctx, name, excludeID)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return apperr.New(apperr.CodeDuplicateName, "已存在同名学科")
		}
	}
	if code != "" {
		dup, err := s.repos.Subjects.ExistsCode(ctx, code, excludeID)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return apperr.New(apperr.CodeDuplicateName, "已存在相同的学科编码")
		}
	}
	return nil
}

// ListMembers 列出学科成员。
func (s *SubjectService) ListMembers(ctx context.Context, user *model.User, subjectID uint) ([]schema.MemberResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}
	list, err := s.repos.Subjects.ListMembers(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	out := make([]schema.MemberResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewMemberResponse(&list[i]))
	}
	return out, nil
}

// UpsertMember 添加成员或修改其角色。
func (s *SubjectService) UpsertMember(ctx context.Context, user *model.User, subjectID uint, req *schema.MemberUpsertRequest) error {
	if err := authorize(user, model.PermManageMembers, subjectID); err != nil {
		return err
	}
	if !req.Role.Valid() {
		return apperr.New(apperr.CodeInvalidParam, "非法的角色")
	}
	if _, err := s.repos.Subjects.Get(ctx, subjectID); err != nil {
		return err
	}
	if _, err := s.repos.Users.Get(ctx, req.UserID); err != nil {
		return err
	}
	if err := s.repos.Subjects.AddMember(ctx, subjectID, req.UserID, req.Role); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

// RemoveMember 移除成员。
//
// 额外约束：不允许移除学科的最后一名负责人。否则该学科会变成
// "只有只读成员"的状态，谁也改不了配置，只能靠管理员来救。
func (s *SubjectService) RemoveMember(ctx context.Context, user *model.User, subjectID, targetUserID uint) error {
	if err := authorize(user, model.PermManageMembers, subjectID); err != nil {
		return err
	}

	members, err := s.repos.Subjects.ListMembers(ctx, subjectID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	var target *model.SubjectMember
	managerCount := 0
	for i := range members {
		if members[i].Role == model.SubjectRoleManager {
			managerCount++
		}
		if members[i].UserID == targetUserID {
			target = &members[i]
		}
	}
	if target == nil {
		return apperr.ErrNotFound.WithDetail("该用户不是本学科成员")
	}
	if target.Role == model.SubjectRoleManager && managerCount <= 1 {
		return apperr.New(apperr.CodeCannotSelfLock, "学科至少需要保留一名负责人")
	}

	if err := s.repos.Subjects.RemoveMember(ctx, subjectID, targetUserID); err != nil {
		return apperr.From(err)
	}
	return nil
}
