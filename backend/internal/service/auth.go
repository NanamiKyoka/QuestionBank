package service

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/hash"
	"github.com/NanamiKyoka/questionmind/internal/pkg/jwtx"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// setupClaimKey 首次初始化的原子标记键。
const setupClaimKey = "system_initialized"

// AuthService 认证与账号用例。
type AuthService struct {
	repos  *Repos
	tokens *jwtx.Manager
	// appName 仅用于初始化状态接口回显。
	appName string
	// state 未初始化状态的进程内缓存，被 SetupGuard 中间件读取。
	state *SetupState
}

// NewAuthService 构造。
func NewAuthService(repos *Repos, tokens *jwtx.Manager, appName string, state *SetupState) *AuthService {
	return &AuthService{repos: repos, tokens: tokens, appName: appName, state: state}
}

// State 暴露状态供中间件与路由使用。
func (s *AuthService) State() *SetupState { return s.state }

// SetupStatus 返回首次运行状态。
//
// 判定依据是"是否已存在任何用户"而不是某个标志位：
// 标志位可能与真实数据不一致（手工删库、导入 dump），而用户表是唯一的真相。
func (s *AuthService) SetupStatus(ctx context.Context) (*schema.SetupStatusResponse, error) {
	n, err := s.repos.Users.Count(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	needs := n == 0
	// 顺带刷新进程内缓存，保证状态与实际数据一致。
	s.state.Set(needs)
	return &schema.SetupStatusResponse{
		NeedsSetup:          needs,
		RegistrationEnabled: s.repos.Settings.GetBool(ctx, model.SettingRegistrationOpen, false),
		AppName:             s.appName,
	}, nil
}

// Setup 首次初始化：创建管理员账号，并可选地创建一个默认学科。
//
// 并发安全：用 ClaimOnce 在数据库层做原子抢占（利用唯一索引），
// 保证即使多个请求同时打进来，也只有一个能真正创建管理员。
// 这是"先查再插"做不到的——两个请求可能都通过 count==0 的检查。
func (s *AuthService) Setup(ctx context.Context, req *schema.SetupRequest) (*schema.LoginResponse, error) {
	var admin *model.User

	err := s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		// 1) 原子抢占初始化标记
		claimed, err := s.repos.Settings.ClaimOnce(ctx, setupClaimKey, time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if !claimed {
			return apperr.New(apperr.CodeForbidden, "系统已经初始化过了，请直接登录")
		}

		// 2) 用户名唯一性（唯一索引兜底，这里给出友好提示）
		taken, err := s.repos.Users.ExistsUsername(ctx, req.Username, 0)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		if taken {
			return apperr.New(apperr.CodeUsernameTaken, "用户名已被占用")
		}

		// 3) 创建管理员
		pwdHash, err := hash.Password(req.Password)
		if err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		nickname := req.Nickname
		if nickname == "" {
			nickname = req.Username
		}
		admin = &model.User{
			Username:     req.Username,
			PasswordHash: pwdHash,
			Nickname:     nickname,
			Email:        req.Email,
			IsSuperuser:  true,
			IsActive:     true,
		}
		if err := s.repos.Users.Create(ctx, admin); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}

		// 4) 可选的默认学科：把管理员设为该学科的负责人
		if req.FirstSubjectName != "" {
			subject := &model.Subject{
				Name:            req.FirstSubjectName,
				ReviewThreshold: 1,
				CreatedBy:       admin.ID,
			}
			if err := s.repos.Subjects.Create(ctx, subject); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
			if err := s.repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
			admin.LastSubjectID = &subject.ID
			if err := s.repos.Subjects.SetLastSubject(ctx, admin.ID, &subject.ID); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 初始化成功，立即解除 setup 门禁，无需重启。
	s.state.Set(false)

	return s.issueToken(ctx, admin)
}

// Login 账号密码登录。
func (s *AuthService) Login(ctx context.Context, req *schema.LoginRequest) (*schema.LoginResponse, error) {
	user, err := s.repos.Users.GetByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 用户不存在时也执行一次等价耗时的哈希校验，
			// 避免通过响应时间差枚举出"哪些用户名存在"。
			hash.DummyVerify()
			return nil, apperr.ErrBadCredentials
		}
		return nil, apperr.ErrInternal.WithCause(err)
	}

	if !hash.Verify(user.PasswordHash, req.Password) {
		return nil, apperr.ErrBadCredentials
	}
	if !user.IsActive {
		return nil, apperr.New(apperr.CodeAccountInactive, "账号已被禁用，请联系管理员")
	}

	// 登录统计。失败不影响登录本身，仅记录日志（由上层处理）。
	now := time.Now().UTC()
	if err := s.repos.Users.TouchLogin(ctx, user.ID, now); err != nil {
		// 刻意不返回错误：统计失败不应阻断用户登录。
		_ = err
	} else {
		user.LastLoginAt = &now
		user.LoginCount++
	}

	// 重新加载成员关系，让响应里带上学科角色。
	full, err := s.repos.Users.GetWithMemberships(ctx, user.ID)
	if err != nil {
		full = user
	}
	return s.issueToken(ctx, full)
}

// issueToken 签发令牌并组装响应。
func (s *AuthService) issueToken(ctx context.Context, user *model.User) (*schema.LoginResponse, error) {
	token, expires, err := s.tokens.Generate(user.ID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	return &schema.LoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresAt:   expires,
		User:        schema.NewUserResponse(user, true),
	}, nil
}

// Me 返回当前用户信息（含学科角色）。
func (s *AuthService) Me(ctx context.Context, userID uint) (*schema.UserResponse, error) {
	user, err := s.repos.Users.GetWithMemberships(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrUnauthorized
		}
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp := schema.NewUserResponse(user, true)
	return &resp, nil
}

// UpdateMe 修改自己的资料。
//
// 只接受 UpdateMeRequest 这一份白名单 DTO——用户名、是否管理员、是否启用
// 都不在其中，因此不存在"请求体里塞了 is_superuser 就被写进去"的可能。
func (s *AuthService) UpdateMe(ctx context.Context, userID uint, req *schema.UpdateMeRequest) (*schema.UserResponse, error) {
	fields := req.Fields()
	if len(fields) > 0 {
		if err := s.repos.Users.UpdateFields(ctx, userID, fields); err != nil {
			return nil, err
		}
	}
	return s.Me(ctx, userID)
}

// ChangePassword 修改自己的密码。
func (s *AuthService) ChangePassword(ctx context.Context, userID uint, req *schema.ChangePasswordRequest) error {
	user, err := s.repos.Users.GetWithMemberships(ctx, userID)
	if err != nil {
		return apperr.ErrUnauthorized
	}
	if !hash.Verify(user.PasswordHash, req.CurrentPassword) {
		return apperr.New(apperr.CodeBadCredentials, "当前密码不正确")
	}
	if req.CurrentPassword == req.NewPassword {
		return apperr.New(apperr.CodeWeakPassword, "新密码不能与当前密码相同")
	}

	newHash, err := hash.Password(req.NewPassword)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return s.repos.Users.UpdateFields(ctx, userID, map[string]any{"password_hash": newHash})
}

// UpdateLastSubject 记录当前工作学科。
func (s *AuthService) UpdateLastSubject(ctx context.Context, userID, subjectID uint) error {
	// 非管理员必须是自己有权限的学科，否则前端会"记住"一个进不去的学科。
	user, err := s.repos.Users.GetWithMemberships(ctx, userID)
	if err != nil {
		return apperr.ErrUnauthorized
	}
	if subjectID != 0 && !user.IsSuperuser {
		if _, ok := user.RoleInSubject(subjectID); !ok {
			return apperr.ErrForbidden.WithDetail("你不是该学科的成员")
		}
	}
	var ptr *uint
	if subjectID != 0 {
		ptr = &subjectID
	}
	if err := s.repos.Subjects.SetLastSubject(ctx, userID, ptr); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}
