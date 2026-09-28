package service

import "testing"

// 审计日志是给人看的，动作名必须一眼能读懂。
// 这张用例表同时也是"路由命名是否合理"的一份检查。
func TestDeriveAction(t *testing.T) {
	cases := []struct {
		method   string
		fullPath string
		action   string
		resource string
	}{
		// 顶层集合与单项
		{"POST", "/api/v1/subjects", "subject.create", "subject"},
		{"PUT", "/api/v1/subjects/:subject_id", "subject.update", "subject"},
		{"DELETE", "/api/v1/subjects/:subject_id", "subject.delete", "subject"},

		// 层级路由：第一段只是作用域，真正的资源在后面
		{"POST", "/api/v1/subjects/:subject_id/questions", "question.create", "question"},
		{"GET", "/api/v1/subjects/:subject_id/questions", "question.view", "question"},
		{"POST", "/api/v1/subjects/:subject_id/knowledge-points", "knowledge_point.create", "knowledge_point"},
		{"POST", "/api/v1/subjects/:subject_id/tag-categories", "tag_category.create", "tag_category"},
		{"GET", "/api/v1/subjects/:subject_id/papers", "paper.view", "paper"},
		// stats 不是"学科的一个视图"，而是统计资源本身。
		{"GET", "/api/v1/subjects/:subject_id/stats", "stat.view", "stat"},
		{"GET", "/api/v1/subjects/:subject_id/imports", "import.view", "import"},

		// 单项操作
		{"PUT", "/api/v1/questions/:id", "question.update", "question"},
		{"DELETE", "/api/v1/tags/:id", "tag.delete", "tag"},

		// POST 到具体资源 = 自定义动作
		{"POST", "/api/v1/questions/:id/review", "question.review", "question"},
		{"POST", "/api/v1/questions/:id/submit", "question.submit", "question"},
		{"POST", "/api/v1/papers/:id/restore", "paper.restore", "paper"},
		{"POST", "/api/v1/papers/:id/duplicate", "paper.duplicate", "paper"},
		{"POST", "/api/v1/users/:id/password", "user.reset_password", "user"},
		{"POST", "/api/v1/knowledge-points/:id/move", "knowledge_point.move", "knowledge_point"},

		// 嵌套子资源
		{"PUT", "/api/v1/papers/:id/nodes", "paper.update_nodes", "paper"},
		{"POST", "/api/v1/papers/:id/versions", "paper.finalize_version", "paper"},
		// members 后面直接跟 :user_id，因此末段是参数，动作回落到 update。
		{"PUT", "/api/v1/subjects/:subject_id/members/:user_id", "member.update", "member"},

		// 带前缀的模块：前缀剥掉后暴露的是真正的资源名
		{"POST", "/api/v1/ai/providers", "provider.create", "provider"},
		{"DELETE", "/api/v1/ai/models/:id", "model.delete", "model"},
		{"POST", "/api/v1/chat/sessions", "session.create", "session"},
		{"POST", "/api/v1/chat/sessions/:id/messages", "session.send_message", "session"},
		{"POST", "/api/v1/chat/runs/:run_id/tool-result", "run.tool_result", "run"},

		// 末段是已知动作时，即使路径里没有 :id 也要识别成动作
		{"POST", "/api/v1/subjects/:subject_id/knowledge-points/reindex", "knowledge_point.reindex", "knowledge_point"},
		{"POST", "/api/v1/subjects/:subject_id/knowledge-points/search", "knowledge_point.search", "knowledge_point"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.fullPath, func(t *testing.T) {
			action, resource := DeriveAction(tc.fullPath, tc.method)
			if action != tc.action {
				t.Fatalf("动作名应为 %q，实际 %q", tc.action, action)
			}
			if resource != tc.resource {
				t.Fatalf("资源名应为 %q，实际 %q", tc.resource, resource)
			}
		})
	}
}

func TestDeriveActionDegradesGracefully(t *testing.T) {
	// 解析不出资源时也要给出一个可用的动作名，不能是空串。
	for _, path := range []string{"", "/", "/api/v1", "/api/v1/"} {
		action, _ := DeriveAction(path, "POST")
		if action == "" {
			t.Fatalf("路径 %q 应至少给出一个动作名", path)
		}
	}
	// 未知方法也要能处理。
	if action, _ := DeriveAction("/api/v1/questions/:id", "OPTIONS"); action == "" {
		t.Fatal("未知方法应给出动作名")
	}
}
