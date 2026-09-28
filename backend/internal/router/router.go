// Package router 组装路由与中间件链。
package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/handler"
	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/jwtx"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/service"
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

	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, apiPrefix) {
			response.Fail(c, apperr.ErrNotFound.WithDetail("接口不存在"))
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
		q.GET("/:id", h.Get)
		q.PUT("/:id", h.Update)
		q.POST("/:id/restore", h.Restore)
		q.POST("/:id/submit", h.Submit)
		q.POST("/:id/review", h.Review)
		q.GET("/:id/review-logs", h.ReviewLogs)
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
}
