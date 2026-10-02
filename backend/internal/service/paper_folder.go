package service

import (
	"context"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// PaperService 组卷用例。
type PaperService struct {
	repos *Repos
}

func NewPaperService(repos *Repos) *PaperService { return &PaperService{repos: repos} }

// resolveScope 解析"空间"归属。
//
// 空间不是一张表，而是 (subject_id, scope, owner_id) 三元组：
//   - personal：归属具体用户，owner_id 由**服务端**填当前用户，客户端无法伪造；
//   - shared：全学科共有，owner_id 为 NULL。
//
// 客户端只能表达"我要个人空间还是共享空间"，不能指定 owner。
func (s *PaperService) resolveScope(user *model.User, requested model.PaperScope) (model.PaperScope, *uint, error) {
	scope := requested
	if scope == "" {
		scope = model.PaperScopePersonal
	}
	if !scope.Valid() {
		return "", nil, apperr.New(apperr.CodeInvalidParam, "非法的空间类型").
			WithDetail("scope=%s", requested)
	}
	if scope == model.PaperScopePersonal {
		owner := user.ID
		return scope, &owner, nil
	}
	return scope, nil, nil
}

// canWrite 判断用户能否修改某份试卷。
func (s *PaperService) canWrite(user *model.User, p *model.Paper) bool {
	if user.IsSuperuser {
		return true
	}
	if !user.Can(model.PermEditQuestion, p.SubjectID) {
		return false
	}
	// 个人空间里只能动自己的卷子；共享空间里学科内有编辑权即可。
	if p.Scope == model.PaperScopePersonal {
		return p.OwnerID != nil && *p.OwnerID == user.ID
	}
	return true
}

// canDelete 删除比编辑更严格：个人空间仅本人，共享空间需学科负责人。
func (s *PaperService) canDelete(user *model.User, p *model.Paper) bool {
	if user.IsSuperuser {
		return true
	}
	if p.Scope == model.PaperScopePersonal {
		return p.OwnerID != nil && *p.OwnerID == user.ID
	}
	return user.Can(model.PermManageSubject, p.SubjectID)
}

// ---- 目录 ----

// FolderTree 返回某空间下的目录树。
func (s *PaperService) FolderTree(ctx context.Context, user *model.User, subjectID uint, requested model.PaperScope) ([]schema.FolderResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}
	scope, ownerID, err := s.resolveScope(user, requested)
	if err != nil {
		return nil, err
	}

	list, err := s.repos.PaperFolders.List(ctx, subjectID, scope, ownerID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	counts, err := s.repos.PaperFolders.PaperCountsByFolder(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	items := make([]schema.FolderResponse, 0, len(list))
	for i := range list {
		item := schema.NewFolderResponse(&list[i])
		item.PaperCount = int(counts[item.ID])
		items = append(items, item)
	}
	return buildFolderTree(items), nil
}

// buildFolderTree 把扁平目录列表组装成树。
func buildFolderTree(items []schema.FolderResponse) []schema.FolderResponse {
	children := make(map[uint][]schema.FolderResponse, len(items))
	// 显式分配而不是留 nil：nil slice 会序列化成 JSON null，
	// 前端拿到 null 之后 for...of 直接抛 "list is not iterable"。
	// 空目录树是首次进入试卷页的常态，不是异常。
	roots := make([]schema.FolderResponse, 0, len(items))

	for _, it := range items {
		if it.ParentID == nil {
			roots = append(roots, it)
			continue
		}
		children[*it.ParentID] = append(children[*it.ParentID], it)
	}

	var attach func(n *schema.FolderResponse)
	attach = func(n *schema.FolderResponse) {
		if kids, ok := children[n.ID]; ok {
			n.Children = kids
			for i := range n.Children {
				attach(&n.Children[i])
			}
		}
	}
	for i := range roots {
		attach(&roots[i])
	}
	return roots
}

// CreateFolder 新建目录。
func (s *PaperService) CreateFolder(ctx context.Context, user *model.User, subjectID uint, req *schema.FolderCreateRequest) (*schema.FolderResponse, error) {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}
	scope, ownerID, err := s.resolveScope(user, req.Scope)
	if err != nil {
		return nil, err
	}

	if req.ParentID != nil {
		parent, err := s.repos.PaperFolders.Get(ctx, *req.ParentID)
		if err != nil {
			return nil, err
		}
		if parent.SubjectID != subjectID || !sameSpace(parent, scope, ownerID) {
			return nil, apperr.New(apperr.CodeCrossSubject, "父目录不在同一个空间下")
		}
	}

	if err := s.checkFolderName(ctx, subjectID, scope, ownerID, req.ParentID, req.Name, 0); err != nil {
		return nil, err
	}

	f := &model.PaperFolder{
		SubjectID: subjectID,
		Scope:     scope,
		OwnerID:   ownerID,
		ParentID:  req.ParentID,
		Name:      req.Name,
		CreatedBy: user.ID,
	}
	if err := s.repos.PaperFolders.Create(ctx, f); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	resp := schema.NewFolderResponse(f)
	return &resp, nil
}

// UpdateFolder 重命名或移动目录。
func (s *PaperService) UpdateFolder(ctx context.Context, user *model.User, id uint, req *schema.FolderUpdateRequest) (*schema.FolderResponse, error) {
	folder, err := s.repos.PaperFolders.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.canWriteFolder(user, folder) {
		return nil, apperr.ErrForbidden
	}

	fields := make(map[string]any, 2)
	name := folder.Name
	if req.Name != nil {
		name = *req.Name
		fields["name"] = name
	}

	targetParent := folder.ParentID
	switch {
	case req.ClearParent:
		targetParent = nil
		fields["parent_id"] = nil
	case req.ParentID != nil:
		if *req.ParentID == folder.ID {
			return nil, apperr.New(apperr.CodeInvalidParam, "不能把目录移动到自己下面")
		}
		// 移动到自己子目录下会让树成环，之后永远走不出来。
		descendant, err := s.repos.PaperFolders.IsDescendant(ctx, folder.SubjectID, folder.ID, *req.ParentID)
		if err != nil {
			return nil, apperr.From(err)
		}
		if descendant {
			return nil, apperr.New(apperr.CodeInvalidParam, "不能把目录移动到它自己的子目录下")
		}
		parent, err := s.repos.PaperFolders.Get(ctx, *req.ParentID)
		if err != nil {
			return nil, err
		}
		if parent.SubjectID != folder.SubjectID || !sameSpace(parent, folder.Scope, folder.OwnerID) {
			return nil, apperr.New(apperr.CodeCrossSubject, "目标目录不在同一个空间下")
		}
		targetParent = req.ParentID
		fields["parent_id"] = *req.ParentID
	}

	if req.Name != nil || req.ParentID != nil || req.ClearParent {
		if err := s.checkFolderName(ctx, folder.SubjectID, folder.Scope, folder.OwnerID, targetParent, name, folder.ID); err != nil {
			return nil, err
		}
	}

	if err := s.repos.PaperFolders.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}

	updated, err := s.repos.PaperFolders.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewFolderResponse(updated)
	return &resp, nil
}

// DeleteFolder 删除目录。非空目录拒绝删除。
//
// 之所以不级联删除：目录里可能有几十份卷子，一次误点全没了，
// 而且软删除后从回收站恢复还要连带恢复目录结构，容易出错。
// 让用户先清空目录是更安全的交互。
func (s *PaperService) DeleteFolder(ctx context.Context, user *model.User, id uint) error {
	folder, err := s.repos.PaperFolders.Get(ctx, id)
	if err != nil {
		return err
	}
	if !s.canWriteFolder(user, folder) {
		return apperr.ErrForbidden
	}

	children, err := s.repos.PaperFolders.CountChildren(ctx, id)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	papers, err := s.repos.PaperFolders.CountPapers(ctx, id)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if children > 0 || papers > 0 {
		return apperr.New(apperr.CodeFolderNotEmpty, "目录不为空，无法删除").
			WithDetail("子目录 %d 个，试卷 %d 份", children, papers)
	}

	if err := s.repos.PaperFolders.SoftDelete(ctx, id); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

func (s *PaperService) canWriteFolder(user *model.User, f *model.PaperFolder) bool {
	if user.IsSuperuser {
		return true
	}
	if !user.Can(model.PermEditQuestion, f.SubjectID) {
		return false
	}
	if f.Scope == model.PaperScopePersonal {
		return f.OwnerID != nil && *f.OwnerID == user.ID
	}
	return true
}

func (s *PaperService) checkFolderName(ctx context.Context, subjectID uint, scope model.PaperScope, ownerID *uint, parentID *uint, name string, excludeID uint) error {
	dup, err := s.repos.PaperFolders.ExistsName(ctx, subjectID, scope, ownerID, parentID, name, excludeID)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	if dup {
		return apperr.New(apperr.CodeDuplicateName, "同级下已存在同名目录")
	}
	return nil
}

// sameSpace 判断目录与给定空间是否属于同一空间。
func sameSpace(f *model.PaperFolder, scope model.PaperScope, ownerID *uint) bool {
	if f.Scope != scope {
		return false
	}
	if ownerID == nil {
		return f.OwnerID == nil
	}
	return f.OwnerID != nil && *f.OwnerID == *ownerID
}
