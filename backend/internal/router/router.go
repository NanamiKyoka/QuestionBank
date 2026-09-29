// Package router 组装路由与中间件链。
package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/handler"
	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/jwtx"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/service"
	"github.com/NanamiKyoka/questionmind/internal/web"
)

// Deps 路由依赖。
type Deps struct {
	Config *config.Config
	DB     *gorm.DB
	Tokens *jwtx.Manager

	Auth            *service.AuthService
	Subjects        *service.SubjectService
	KnowledgePoints *service.KnowledgePointService
	Tags            *service.TagService
	Questions       *service.QuestionService
	Users           *service.UserService
	Papers          *service.PaperService
	Exports         *service.ExportService
	AIConfig        *service.AIConfigService
	Imports         *service.ImportService
	Chat            *service.ChatService
	Vectors         *service.VectorService
	Stats           *service.StatsService
	Activities      *service.ActivityService
	Settings        *service.SettingsService
	Prompts         *service.PromptService
	Uploads         *service.UploadService
	Media           *storage.Local
}

const apiPrefix = "/api/v1"

// setupExemptPaths 未初始化时也必须放行的路径，否则前端既探测不到状态也完不成初始化。
var setupExemptPaths = []string{
	apiPrefix + "/system/health",
	apiPrefix + "/system/version",
	apiPrefix + "/system/setup-status",
	apiPrefix + "/auth/setup",
	apiPrefix + "/auth/login",
}

// New 构建 HTTP 引擎。
func New(d Deps) *gin.Engine {
	if d.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	r := gin.New()
	r.RedirectTrailingSlash = false

	r.Use(
		middleware.RequestID(),
		middleware.Recovery(),
		middleware.AccessLog(),
		middleware.CORS(d.Config.Server.CORSAllowOrigins),
	)
	r.Use(middleware.Auth(repository.NewUserRepo(d.DB), d.Tokens))

	// 审计日志记录写操作。放在 Auth 之后，才能拿到当前用户。
	// 排除登录与初始化：这两条要么还没有用户身份，要么会记录密码相关操作。
	r.Use(middleware.ActivityLogger(d.Activities, []string{
		apiPrefix + "/auth/login",
		apiPrefix + "/auth/setup",
		apiPrefix + "/setup",
	}))

	system := handler.NewSystemHandler(d.Auth, d.DB, d.Config.App.Name)

	// 容器探活放在 /api 之外，避免被 setup 门禁拦住。
	r.GET("/healthz", system.Health)

	v1 := r.Group(apiPrefix)
	v1.Use(setupGuard(d.Auth.State()))

	registerSystemRoutes(v1, system)
	registerAuthRoutes(v1, handler.NewAuthHandler(d.Auth, d.Config.JWT.AccessTTL, d.Config.IsProduction()))
	registerSubjectRoutes(v1, d)
	registerKnowledgePointRoutes(v1, d)
	registerTagRoutes(v1, d)
	registerQuestionRoutes(v1, d)
	registerUserRoutes(v1, d)
	registerPaperRoutes(v1, d)
	registerAIConfigRoutes(v1, d)
	registerImportRoutes(v1, d)
	registerChatRoutes(v1, d)
	registerVectorRoutes(v1, d)
	registerStatsRoutes(v1, d)
	registerActivityRoutes(v1, d)
	registerSettingsRoutes(v1, d)
	registerPromptRoutes(v1, d)
	registerUploadRoutes(v1, d)

	// 媒体文件（题目配图）直接由后端提供。
	// 不做鉴权：地址里含 16 字节随机名，猜不出来；而图片要能嵌进
	// 导出的文档与浏览器缓存，带上鉴权头反而处处掣肘。
	if d.Media != nil {
		r.StaticFS(d.Media.PublicPrefix(), http.Dir(d.Media.MediaDir()))
	}

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, apiPrefix) {
			response.Fail(c, apperr.ErrNotFound.WithDetail("接口不存在"))
			return
		}
		// 前端产物已内嵌时，其余路径交给单页应用处理。
		if web.Available() {
			// gin 在调用 NoRoute 之前已经用 serveError 把状态码预置成 404。
			// 前端路由必须先把状态改回 200，否则页面能渲染出来，
			// 但浏览器控制台会一直报 404——这类问题很难从现象定位到原因。
			if web.IsSPARoute(path) {
				c.Status(http.StatusOK)
			}
			web.ServeSPA().ServeHTTP(c.Writer, c.Request)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"message": "not found"})
	})

	return r
}

// setupGuard 未完成初始化时统一返回 503，让前端跳转到安装向导。
func setupGuard(state *service.SetupState) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !state.Required() {
			c.Next()
			return
		}
		for _, exempt := range setupExemptPaths {
			if c.Request.URL.Path == exempt {
				c.Next()
				return
			}
		}
		response.Fail(c, apperr.ErrSetupRequired)
	}
}

func registerSystemRoutes(g *gin.RouterGroup, h *handler.SystemHandler) {
	sys := g.Group("/system")
	sys.GET("/health", h.Health)
	sys.GET("/version", h.Version)
	sys.GET("/setup-status", h.SetupStatus)
}

func registerAuthRoutes(g *gin.RouterGroup, h *handler.AuthHandler) {
	auth := g.Group("/auth")
	auth.POST("/setup", h.Setup)
	auth.POST("/login", h.Login)

	me := auth.Group("")
	me.Use(middleware.RequireAuth())
	me.POST("/logout", h.Logout)
	me.GET("/me", h.Me)
	me.PATCH("/me", h.UpdateMe)
	me.POST("/me/password", h.ChangePassword)
	me.POST("/me/last-subject", h.UpdateLastSubject)
}

func registerSubjectRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewSubjectHandler(d.Subjects)

	subjects := g.Group("/subjects")
	subjects.Use(middleware.RequireAuth())
	{
		subjects.GET("", h.List)
		subjects.POST("", h.Create)
		subjects.GET("/:subject_id", h.Get)
		subjects.PUT("/:subject_id", h.Update)
		subjects.DELETE("/:subject_id", h.Delete)
		subjects.GET("/:subject_id/members", h.ListMembers)
		subjects.PUT("/:subject_id/members/:user_id", h.UpsertMember)
		subjects.DELETE("/:subject_id/members/:user_id", h.RemoveMember)
	}
}

func registerKnowledgePointRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewKnowledgePointHandler(d.KnowledgePoints)

	// 列表与新建挂在学科下；按 id 的操作放在顶层，避免与 /subjects 的通配符冲突。
	g.GET("/subjects/:subject_id/knowledge-points", middleware.RequireAuth(), h.Tree)
	g.POST("/subjects/:subject_id/knowledge-points", middleware.RequireAuth(), h.Create)

	kps := g.Group("/knowledge-points")
	kps.Use(middleware.RequireAuth())
	{
		kps.PUT("/:id", h.Update)
		kps.PATCH("/:id/move", h.Move)
		kps.DELETE("/:id", h.Delete)
	}
}

func registerTagRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewTagHandler(d.Tags)

	g.GET("/subjects/:subject_id/tag-categories", middleware.RequireAuth(), h.ListCategories)
	g.POST("/subjects/:subject_id/tag-categories", middleware.RequireAuth(), h.CreateCategory)

	cats := g.Group("/tag-categories")
	cats.Use(middleware.RequireAuth())
	{
		cats.PUT("/:id", h.UpdateCategory)
		cats.DELETE("/:id", h.DeleteCategory)
	}

	g.GET("/subjects/:subject_id/tags", middleware.RequireAuth(), h.List)
	g.POST("/subjects/:subject_id/tags", middleware.RequireAuth(), h.Create)

	tags := g.Group("/tags")
	tags.Use(middleware.RequireAuth())
	{
		tags.PUT("/:id", h.Update)
		tags.DELETE("/:id", h.Delete)
	}
}

func registerQuestionRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewQuestionHandler(d.Questions)

	// 列表、新建、统计挂在学科下（统计路径更具体，需先注册）。
	g.GET("/subjects/:subject_id/questions/stats", middleware.RequireAuth(), h.Stats)
	g.GET("/subjects/:subject_id/questions", middleware.RequireAuth(), h.List)
	g.POST("/subjects/:subject_id/questions", middleware.RequireAuth(), h.Create)

	q := g.Group("/questions")
	q.Use(middleware.RequireAuth())
	{
		q.POST("/batch-delete", h.BatchDelete)
		q.POST("/batch-source", h.BatchUpdateSource)
		q.POST("/batch-review", h.BatchReview)
		q.GET("/:id", h.Get)
		q.PUT("/:id", h.Update)
		q.POST("/:id/restore", h.Restore)
		q.POST("/:id/submit", h.Submit)
		q.POST("/:id/review", h.Review)
		q.GET("/:id/review-logs", h.ReviewLogs)
		q.POST("/:id/archive", h.Archive)
		q.POST("/:id/unarchive", h.Unarchive)
	}
}

func registerUserRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewUserHandler(d.Users)

	// 权限自查询挂在 /auth/me 下，既语义正确，也避免与 /users/:id 的
	// 通配符在同一层冲突。
	g.GET("/auth/me/permissions", middleware.RequireAuth(), h.Permissions)

	users := g.Group("/users")
	users.Use(middleware.RequireAuth(), middleware.RequireSuperuser())
	{
		users.GET("", h.List)
		users.POST("", h.Create)
		users.GET("/:id", h.Get)
		users.PUT("/:id", h.Update)
		users.DELETE("/:id", h.Delete)
		users.POST("/:id/password", h.ResetPassword)
	}
}

func registerPaperRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewPaperHandler(d.Papers)

	// 目录与试卷列表挂在学科下；按 id 的操作放在 /papers 顶层。
	g.GET("/subjects/:subject_id/paper-folders", middleware.RequireAuth(), h.FolderTree)
	g.POST("/subjects/:subject_id/paper-folders", middleware.RequireAuth(), h.CreateFolder)
	g.GET("/subjects/:subject_id/papers", middleware.RequireAuth(), h.List)
	g.POST("/subjects/:subject_id/papers", middleware.RequireAuth(), h.Create)

	folders := g.Group("/paper-folders")
	folders.Use(middleware.RequireAuth())
	{
		folders.PUT("/:id", h.UpdateFolder)
		folders.DELETE("/:id", h.DeleteFolder)
	}

	papers := g.Group("/papers")
	papers.Use(middleware.RequireAuth())
	{
		papers.GET("/:id", h.Get)
		papers.PUT("/:id", h.Update)
		papers.DELETE("/:id", h.Delete)
		papers.POST("/:id/restore", h.Restore)
		papers.POST("/:id/duplicate", h.Duplicate)
		papers.PUT("/:id/nodes", h.ReplaceNodes)
		papers.GET("/:id/question-revisions", h.QuestionRevisions)
		papers.POST("/:id/question-nodes/sync", h.SyncQuestionNodes)
		papers.GET("/:id/versions", h.ListVersions)
		papers.POST("/:id/versions", h.Finalize)
		papers.GET("/:id/versions/:version_no", h.GetVersion)
		papers.GET("/:id/events", h.ListEvents)
	}

	// 导出返回二进制附件，因此单独挂一个 handler。
	ex := handler.NewExportHandler(d.Exports)
	papers.GET("/:id/export", ex.ExportPaper)
	papers.GET("/:id/versions/:version_no/export", ex.ExportVersion)
}

func registerAIConfigRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewAIConfigHandler(d.AIConfig)

	// AI 配置涉及密钥与额度，整组仅管理员可用。
	aiGroup := g.Group("/ai")
	aiGroup.Use(middleware.RequireAuth(), middleware.RequireSuperuser())
	{
		aiGroup.GET("/providers", h.ListProviders)
		aiGroup.POST("/providers", h.CreateProvider)
		aiGroup.PUT("/providers/:id", h.UpdateProvider)
		aiGroup.DELETE("/providers/:id", h.DeleteProvider)
		aiGroup.POST("/providers/:id/test", h.TestProvider)

		aiGroup.POST("/models", h.CreateModel)
		aiGroup.PUT("/models/:id", h.UpdateModel)
		aiGroup.DELETE("/models/:id", h.DeleteModel)

		aiGroup.GET("/active-config", h.GetActiveConfig)
		aiGroup.PUT("/active-config", h.SetActiveConfig)
		aiGroup.GET("/interface-types", h.InterfaceTypes)
	}
}

func registerImportRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewImportHandler(d.Imports)

	// 智能录题挂在学科下：抽取与入库都需要学科的编辑权限，由 service 层判定。
	imports := g.Group("/subjects/:subject_id/imports")
	imports.Use(middleware.RequireAuth())
	{
		imports.POST("/extract", h.Extract)
		imports.POST("/commit", h.Commit)
		// upload 是静态路径，必须排在 :task_id 之前。
		imports.POST("/upload", h.Upload)
		// 批量排队：一次收多个文件，立刻返回 202。
		imports.POST("", h.Enqueue)
		imports.GET("", h.ListTasks)
		// queue-status 必须排在 :task_id 之前：gin 的路由树是按段匹配的，
		// 反过来会让 "queue-status" 被 :task_id 接住，然后在 parseID 上 400。
		imports.GET("/queue-status", h.QueueStatus)
		imports.GET("/:task_id/drafts", h.TaskDrafts)
		imports.GET("/:task_id/questions", h.TaskQuestions)
		imports.POST("/:task_id/cancel", h.CancelTask)
		imports.POST("/:task_id/retry", h.RetryTask)
		imports.DELETE("/:task_id", h.DeleteTask)
	}
}

func registerChatRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewChatHandler(d.Chat)

	chat := g.Group("/chat")
	chat.Use(middleware.RequireAuth())
	{
		chat.GET("/sessions", h.ListSessions)
		chat.POST("/sessions", h.CreateSession)
		chat.GET("/sessions/:id", h.GetSession)
		chat.PATCH("/sessions/:id", h.UpdateSession)
		chat.DELETE("/sessions/:id", h.DeleteSession)
		chat.GET("/sessions/:id/messages", h.ListMessages)
		// 这条以 SSE 流式返回。
		chat.POST("/sessions/:id/messages", h.SendMessage)

		// 前端执行完客户端工具后回传，唤醒等待中的运行。
		chat.POST("/runs/:run_id/tool-result", h.ResolveClientTool)
	}
}

func registerActivityRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewActivityHandler(d.Activities)
	// 审计日志含全部用户的操作记录，仅管理员可见。
	g.GET("/activity-logs", middleware.RequireAuth(), h.List)
}

// registerSettingsRoutes 系统设置读写。
//
// 仅管理员可用：这些键决定的是系统级行为（是否开放注册、审核默认阈值），
// 而不是某个学科的偏好。
func registerSettingsRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewSettingsHandler(d.Settings)
	settings := g.Group("/settings")
	settings.Use(middleware.RequireAuth(), middleware.RequireSuperuser())
	{
		settings.GET("", h.List)
		settings.PUT("/:key", h.Update)
	}
}

// registerPromptRoutes 提示词模板与学科覆盖。
//
// 仅管理员可用：提示词是全系统行为，改错一次会同时影响所有学科的录题与对话。
func registerPromptRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewPromptHandler(d.Prompts)
	prompts := g.Group("/prompts")
	prompts.Use(middleware.RequireAuth(), middleware.RequireSuperuser())
	{
		prompts.GET("", h.List)
		prompts.POST("", h.Create)
		prompts.PUT("/:id", h.Update)
		prompts.DELETE("/:id", h.Delete)
	}

	// 通配段名必须与既有路由一致地叫 :subject_id：
	// gin v1.12 不允许同一位置出现两个不同名字的通配段，
	// 用 :sid 会在启动时 panic，而不是报错。
	subjects := g.Group("/subjects/:subject_id")
	subjects.Use(middleware.RequireAuth(), middleware.RequireSuperuser())
	{
		subjects.GET("/prompts", h.SubjectPrompts)
		subjects.PUT("/prompts", h.SaveSubjectPrompts)
	}
}

func registerUploadRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewUploadHandler(d.Uploads)
	up := g.Group("/uploads")
	up.Use(middleware.RequireAuth())
	{
		up.POST("/images", h.UploadImages)
		up.DELETE("/images", h.DeleteImage)
	}
}

func registerStatsRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewStatsHandler(d.Stats)

	g.GET("/subjects/:subject_id/stats", middleware.RequireAuth(), h.Subject)
	g.GET("/stats/overview", middleware.RequireAuth(), h.Global)
}

func registerVectorRoutes(g *gin.RouterGroup, d Deps) {
	h := handler.NewVectorHandler(d.Vectors)

	// 挂在知识点路径下：这几条都是针对某个学科的知识点索引。
	g.GET("/subjects/:subject_id/knowledge-points/vector-status", middleware.RequireAuth(), h.Status)
	g.POST("/subjects/:subject_id/knowledge-points/reindex", middleware.RequireAuth(), h.Reindex)
	g.POST("/subjects/:subject_id/knowledge-points/search", middleware.RequireAuth(), h.Search)
}
