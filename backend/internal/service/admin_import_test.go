package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

func newAdminImportFixture(t *testing.T) (*AdminImportService, *Repos, *model.Subject, *model.User) {
	t.Helper()
	repos := newTestRepos(t)
	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(context.Background(), subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	if err := repos.Subjects.AddMember(context.Background(), subject.ID, admin.ID, model.SubjectRoleManager); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}
	return NewAdminImportService(repos), repos, subject, admin
}

func TestAdminImportUsers(t *testing.T) {
	svc, _, _, admin := newAdminImportFixture(t)
	data, err := buildImportWorkbook("用户导入", []string{"用户名", "姓名", "密码", "管理员"}, [][]string{
		{"teacher01", "张三", "changeme123", ""},
		{"admin02", "李四", "changeme123", "是"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ImportUsers(context.Background(), admin, data)
	if err != nil {
		t.Fatalf("导入用户失败: %v", err)
	}
	if res.Status != "success" || res.Created != 2 || res.Failed != 0 {
		t.Fatalf("结果不符: %+v", res)
	}
	user, err := svc.repos.Users.GetByUsername(context.Background(), "admin02")
	if err != nil {
		t.Fatal(err)
	}
	if !user.IsSuperuser || user.Nickname != "李四" {
		t.Fatalf("管理员标记或姓名未导入: %+v", user)
	}
}

func TestAdminImportTagsCreatesCategories(t *testing.T) {
	svc, _, subject, admin := newAdminImportFixture(t)
	data, err := buildImportWorkbook("标签导入", []string{"名称", "分类", "颜色"}, [][]string{
		{"高考真题", "来源", "#4F81BD"},
		{"易错题", "", ""},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ImportTags(context.Background(), admin, subject.ID, data)
	if err != nil {
		t.Fatalf("导入标签失败: %v", err)
	}
	if res.Status != "success" || res.Created != 2 {
		t.Fatalf("结果不符: %+v", res)
	}
	categories, err := svc.repos.TagCategories.ListBySubject(context.Background(), subject.ID)
	if err != nil || len(categories) != 1 || categories[0].Name != "来源" {
		t.Fatalf("分类未创建: categories=%+v err=%v", categories, err)
	}
}

func TestAdminImportKnowledgePointsIncrementalAndRebuild(t *testing.T) {
	svc, repos, subject, admin := newAdminImportFixture(t)
	headers := []string{"一级目录", "二级目录", "三级目录"}
	rows := [][]string{
		{"代数", "方程", "一元一次方程"},
		{"代数", "方程", "一元二次方程"},
	}
	data, err := buildImportWorkbook("知识点导入", headers, rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ImportKnowledgePoints(context.Background(), admin, subject.ID, "incremental", data)
	if err != nil {
		t.Fatalf("增量导入失败: %v", err)
	}
	if res.Created != 4 {
		t.Fatalf("应创建 4 个节点，实际 %+v", res)
	}
	points, err := repos.KnowledgePoints.ListBySubject(context.Background(), subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := findImportedKP(points, "一元一次方程")
	if child == nil || child.Level != 3 || child.Path == "" {
		t.Fatalf("多级路径未建立: %+v", child)
	}

	rebuild, err := buildImportWorkbook("知识点导入", headers, [][]string{{"几何", "三角形", ""}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = svc.ImportKnowledgePoints(context.Background(), admin, subject.ID, "rebuild", rebuild)
	if err != nil {
		t.Fatalf("重建导入失败: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("应重建 2 个节点，实际 %+v", res)
	}
	points, err = repos.KnowledgePoints.ListBySubject(context.Background(), subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || findImportedKP(points, "代数") != nil {
		t.Fatalf("旧知识点未被重建: %+v", points)
	}
}

func findImportedKP(points []model.KnowledgePoint, name string) *model.KnowledgePoint {
	for i := range points {
		if points[i].Name == name {
			return &points[i]
		}
	}
	return nil
}
