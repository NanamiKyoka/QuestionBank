package service

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/hash"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// UserService 用户与权限用例。
type UserService struct {
	repos *Repos
}

func NewUserService(repos *Repos) *UserService { return &UserService{repos: repos} }

// Permissions 返回当前用户的完整权限视图。
//
// 不校验任何权限：这是"我能做什么"的自我查询，任何登录用户都应该能调。
func (s *UserService) Permissions(ctx context.Context, userID uint) (*schema.MyPermissionsResponse, error) {
	user, err := s.repos.Users.GetWithMemberships(ctx, userID)
	if err != nil {
		return nil, apperr.ErrUnauthorized
	}

	resp := &schema.MyPermissionsResponse{
		IsSuperuser:  user.IsSuperuser,
		Capabilities: schema.GlobalCapabilities(user),
		Memberships:  make([]schema.MembershipResponse, 0, len(user.Memberships)),
	}

	ids, unrestricted := user.SubjectIDs()
	if !unrestricted {
		list := make([]uint, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		resp.AccessibleSubjectIDs = list
	}

	full := schema.NewUserResponse(user, true)
	resp.Memberships = full.Memberships
	return resp, nil
}

// List 用户列表。仅管理员。
func (s *UserService) List(ctx context.Context, actor *model.User, q schema.UserListQuery, limit, offset int) ([]schema.UserDetailResponse, int64, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, 0, err
	}

	list, total, err := s.repos.Users.List(ctx, repository.ListFilter{
		Keyword:   q.Keyword,
		IsActive:  q.IsActive,
		SubjectID: q.SubjectID,
	}, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.UserDetailResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewUserDetailResponse(&list[i]))
	}
	return out, total, nil
}

// Get 单个用户详情。仅管理员。
func (s *UserService) Get(ctx context.Context, actor *model.User, id uint) (*schema.UserDetailResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	user, err := s.repos.Users.GetWithMemberships(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("用户 %d 不存在", id)
		}
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp := schema.NewUserDetailResponse(user)
	return &resp, nil
}

// Create 管理员新建用户。
func (s *UserService) Create(ctx context.Context, actor *model.User, req *schema.UserCreateRequest) (*schema.UserDetailResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	taken, err := s.repos.Users.ExistsUsername(ctx, req.Username, 0)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if taken {
		return nil, apperr.New(apperr.CodeUsernameTaken, "用户名已被占用")
	}

	pwdHash, err := hash.Password(req.Password)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	nickname := req.Nickname
	if nickname == "" {
		nickname = req.Username
	}

	var created *model.User
	err = s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		u := &model.User{
			Username:     req.Username,
			PasswordHash: pwdHash,
			Nickname:     nickname,
			Email:        req.Email,
			IsSuperuser:  req.IsSuperuser,
			IsActive:     true,
		}
		if req.IsActive != nil {
			u.IsActive = *req.IsActive
		}
		if err := s.repos.Users.Create(ctx, u); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		created = u

		// 可选：同时加入某个学科。
		if req.SubjectID != nil {
			if _, err := s.repos.Subjects.Get(ctx, *req.SubjectID); err != nil {
				return err
			}
			role := model.SubjectRoleEditor
			if req.SubjectRole != nil && req.SubjectRole.Valid() {
				role = *req.SubjectRole
			}
			if err := s.repos.Subjects.AddMember(ctx, *req.SubjectID, u.ID, role); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return s.Get(ctx, actor, created.ID)
}

// Update 管理员修改用户。
func (s *UserService) Update(ctx context.Context, actor *model.User, id uint, req *schema.UserAdminUpdateRequest) (*schema.UserDetailResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	target, err := s.repos.Users.GetWithMemberships(ctx, id)
	if err != nil {
		return nil, apperr.ErrNotFound.WithDetail("用户 %d 不存在", id)
	}

	// 关键保护：不允许把最后一个可用管理员降级或禁用，
	// 否则系统会永久失去管理入口，只能改数据库才能恢复。
	if err := s.guardLastSuperuser(ctx, actor, target, req); err != nil {
		return nil, err
	}

	fields := make(map[string]any, 5)
	if req.Nickname != nil {
		fields["nickname"] = *req.Nickname
	}
	if req.Email != nil {
		fields["email"] = *req.Email
	}
	if req.Avatar != nil {
		fields["avatar"] = *req.Avatar
	}
	if req.IsActive != nil {
		fields["is_active"] = *req.IsActive
	}
	if req.IsSuperuser != nil {
		fields["is_superuser"] = *req.IsSuperuser
	}

	if err := s.repos.Users.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}
	return s.Get(ctx, actor, id)
}

// guardLastSuperuser 阻止把最后一个可用管理员降级/禁用/删除。
func (s *UserService) guardLastSuperuser(ctx context.Context, actor, target *model.User, req *schema.UserAdminUpdateRequest) error {
	if !target.IsSuperuser {
		return nil
	}

	willDemote := req.IsSuperuser != nil && !*req.IsSuperuser
	willDisable := req.IsActive != nil && !*req.IsActive
	if !willDemote && !willDisable {
		return nil
	}

	n, err := s.repos.Users.CountSuperusers(ctx)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if n <= 1 {
		return apperr.New(apperr.CodeCannotSelfLock, "系统至少需要保留一名启用的管理员")
	}
	// 自己把自己降级也一样危险：会立刻丢失管理入口。
	if actor.ID == target.ID && willDemote {
		return apperr.New(apperr.CodeCannotSelfLock, "不能取消自己的管理员身份")
	}
	return nil
}

// Delete 软删除用户。仅管理员。
func (s *UserService) Delete(ctx context.Context, actor *model.User, id uint) error {
	if err := authorizeSuperuser(actor); err != nil {
		return err
	}
	if actor.ID == id {
		return apperr.New(apperr.CodeCannotSelfLock, "不能删除自己的账号")
	}

	target, err := s.repos.Users.GetWithMemberships(ctx, id)
	if err != nil {
		return err
	}
	if target.IsSuperuser {
		n, err := s.repos.Users.CountSuperusers(ctx)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if n <= 1 {
			return apperr.New(apperr.CodeCannotSelfLock, "系统至少需要保留一名启用的管理员")
		}
	}

	// 软删除并同时清空成员关系：留着成员关系会让已删除用户仍出现在学科成员列表里。
	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		for _, m := range target.Memberships {
			if err := s.repos.Subjects.RemoveMember(ctx, m.SubjectID, id); err != nil {
				return apperr.From(err)
			}
		}
		if err := s.repos.Users.SoftDelete(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return nil
	})
}

// ResetPassword 管理员重置他人密码。
func (s *UserService) ResetPassword(ctx context.Context, actor *model.User, id uint, req *schema.ResetPasswordRequest) error {
	if err := authorizeSuperuser(actor); err != nil {
		return err
	}
	if _, err := s.repos.Users.Get(ctx, id); err != nil {
		return err
	}

	pwdHash, err := hash.Password(req.NewPassword)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if err := s.repos.Users.UpdateFields(ctx, id, map[string]any{"password_hash": pwdHash}); err != nil {
		return apperr.From(err)
	}
	return nil
}
