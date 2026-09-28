package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// activityQueueSize 审计日志的异步队列长度。
const activityQueueSize = 4096

// ActivityService 操作审计。
//
// 写入是**异步且有损**的：审计日志不能成为请求路径上的阻塞点，
// 更不能因为它写失败就让用户的业务操作失败。队列满时直接丢弃并记一条警告——
// 丢掉几条审计记录，远比让用户在保存题目时卡住或报错要好。
type ActivityService struct {
	repos *Repos

	queue chan *model.ActivityLog
	once  sync.Once
}

func NewActivityService(repos *Repos) *ActivityService {
	return &ActivityService{
		repos: repos,
		queue: make(chan *model.ActivityLog, activityQueueSize),
	}
}

// Start 启动后台写入协程。ctx 结束时协程退出。
//
// 由 main 在启动时调用一次；重复调用无效果（once 保护）。
func (s *ActivityService) Start(ctx context.Context) {
	s.once.Do(func() {
		go s.consume(ctx)
	})
}

func (s *ActivityService) consume(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			// 退出前把队列里剩下的尽量写完。
			s.drain()
			return
		case entry := <-s.queue:
			s.write(entry)
		}
	}
}

// drain 排空队列。
func (s *ActivityService) drain() {
	for {
		select {
		case entry := <-s.queue:
			s.write(entry)
		default:
			return
		}
	}
}

func (s *ActivityService) write(entry *model.ActivityLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.repos.Activities.Create(ctx, entry); err != nil {
		slog.Warn("写入审计日志失败", "action", entry.Action, "error", err)
	}
}

// Record 入队一条审计记录。队列满时丢弃并记警告。
//
// 参数直接收 model.ActivityLog 而不是自定义的 Entry 结构：
// 这样中间件可以用 model 包这个叶子包构造记录，不必反向依赖 service，
// 也就避免了 middleware ↔ service 的循环依赖。
func (s *ActivityService) Record(entry *model.ActivityLog) {
	if entry == nil {
		return
	}
	entry.UserAgent = truncateRunes(entry.UserAgent, 200)

	select {
	case s.queue <- entry:
	default:
		// 队列满说明写入速度跟不上。宁可丢日志也不阻塞请求。
		slog.Warn("审计日志队列已满，丢弃记录", "action", entry.Action)
	}
}

// List 查询审计日志。仅管理员。
func (s *ActivityService) List(ctx context.Context, actor *model.User, q schema.ActivityLogQuery, limit, offset int) ([]schema.ActivityLogResponse, int64, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, 0, err
	}

	list, total, err := s.repos.Activities.List(ctx, repository.ActivityFilter{
		UserID:   q.UserID,
		Action:   q.Action,
		Resource: q.Resource,
		Keyword:  q.Keyword,
		Success:  q.Success,
		Since:    q.Since,
		Until:    q.Until,
	}, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.ActivityLogResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewActivityLogResponse(&list[i]))
	}
	return out, total, nil
}

// actionVerbs 把路径末段映射成人可读的动作名。
//
// 没有这张表时，POST /questions/:id/review 会被记成 "question.review"
// （还行），但 POST /chat/sessions/:id/messages 会变成
// "session.messages"——审计日志是给人看的，宁可多写一张表。
var actionVerbs = map[string]string{
	"messages":    "send_message",
	"password":    "reset_password",
	"review":      "review",
	"submit":      "submit",
	"reindex":     "reindex",
	"search":      "search",
	"sync":        "sync",
	"duplicate":   "duplicate",
	"restore":     "restore",
	"test":        "test",
	"move":        "move",
	"commit":      "commit",
	"extract":     "extract",
	"nodes":       "update_nodes",
	"versions":    "finalize_version",
	"members":     "update_member",
	"prompts":     "update_prompt",
	"models":      "update_model",
	"tool-result": "tool_result",
}

// resourceAliases 路径首段与真实资源名的映射。
//
// 有些路由的首段只是"容器"：/subjects/:subject_id/questions 里
// subjects 只是作用域，真正的资源是 questions。
var resourceAliases = map[string]string{
	"knowledge-points": "knowledge_point",
	"tag-categories":   "tag_category",
	"paper-folders":    "paper_folder",
	"imports":          "import",
	"activity-logs":    "activity_log",
	"ai":               "ai_config",
	"settings":         "system_setting",
	"system":           "system",
}

// DeriveAction 从路由模式推断动作名，如 question.create。
//
// 用的是 gin 的 FullPath（路由**模式**而不是实际路径），因此
// "/api/v1/questions/:id" 能被稳定识别成 questions 资源，
// 不必从实际路径里猜哪一段是 id。
//
// 这是启发式规则：解析不出来时退化为"方法 + 首段"，不影响记录本身。
func DeriveAction(fullPath, method string) (action, resource string) {
	parts := pathSegments(fullPath)

	// 层级路由的第一段常常只是作用域容器，剥掉它才是真正的资源。
	// 例如 /subjects/:subject_id/questions → questions。
	if len(parts) >= 2 && parts[0] == "subjects" && strings.HasPrefix(parts[1], ":") {
		parts = parts[2:]
	} else if len(parts) >= 2 && (parts[0] == "ai" || parts[0] == "chat") {
		// /ai/providers、/chat/sessions 这类前缀也剥掉。
		parts = parts[1:]
	}

	if len(parts) == 0 {
		resource = "subject"
		return resource + "." + simpleVerb(method), resource
	}

	resource = normalizeResource(parts[0])
	rest := parts[1:]

	// 路径里有 :id → 针对具体资源。
	hasParam := false
	for _, p := range rest {
		if strings.HasPrefix(p, ":") {
			hasParam = true
			break
		}
	}

	verb := simpleVerb(method)
	last := parts[len(parts)-1]

	switch {
	case len(parts) > 1 && !strings.HasPrefix(last, ":"):
		// 末段是已知动作名时一律采用，**不管路径里有没有 :id**。
		// 例如 knowledge-points/reindex、papers/:id/versions 都没有
		// 紧邻末段的参数，但它们显然是动作而不是"新建"。
		if mapped, ok := actionVerbs[last]; ok {
			verb = mapped
		} else if method == "POST" && hasParam {
			// POST 到具体资源且末段是未知词：当作自定义动作。
			verb = last
		}
	}

	return resource + "." + verb, resource
}

// pathSegments 切分路径并去掉 api / v1 前缀。
func pathSegments(fullPath string) []string {
	parts := strings.Split(strings.Trim(fullPath, "/"), "/")
	if len(parts) >= 2 && parts[0] == "api" {
		parts = parts[2:]
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normalizeResource 把路径段规范成资源名。
func normalizeResource(seg string) string {
	if alias, ok := resourceAliases[seg]; ok {
		return alias
	}
	// 去掉复数后缀。保留 -s 结尾但实为单数的词（如 "status"）。
	if strings.HasSuffix(seg, "s") && !strings.HasSuffix(seg, "ss") && len(seg) > 2 {
		return strings.TrimSuffix(seg, "s")
	}
	return seg
}

func simpleVerb(method string) string {
	switch method {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	case "GET":
		return "view"
	default:
		return strings.ToLower(method)
	}
}
