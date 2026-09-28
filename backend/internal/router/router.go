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
	Auth   *service.AuthService
	Tokens *jwtx.Manager
}

// API 前缀。
const apiPrefix = "/api/v1"

// setupExemptPaths 未初始化时也必须放行的路径。
// 否则前端无法探测状态、也无法完成初始化。
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

	// 中间件顺序即执行顺序，不要随意调整。
	r.Use(
		middleware.RequestID(),
		middleware.Recovery(),
		middleware.AccessLog(),
		middleware.CORS(d.Config.Server.CORSAllowOrigins),
	)

	userRepo := repository.NewUserRepo(d.DB)
	r.Use(middleware.Auth(userRepo, d.Tokens))

	system := handler.NewSystemHandler(d.Auth, d.DB, d.Config.App.Name)
	auth := handler.NewAuthHandler(d.Auth, d.Config.JWT.AccessTTL, d.Config.IsProduction())

	// 容器探活不放在 /api 下，避免被 setup 门禁拦住。
	r.GET("/healthz", system.Health)

	v1 := r.Group(apiPrefix)
	v1.Use(setupGuard(d.Auth.State()))

	registerSystemRoutes(v1, system)
	registerAuthRoutes(v1, auth)

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
		path := c.Request.URL.Path
		for _, exempt := range setupExemptPaths {
			if path == exempt {
				c.Next()
				return
			}
		}
		response.Fail(c, apperr.ErrSetupRequired)
	}
}

func registerSystemRoutes(g *gin.RouterGroup, h *handler.SystemHandler) {
	sys := g.Group("/system")
	{
		sys.GET("/health", h.Health)
		sys.GET("/version", h.Version)
		sys.GET("/setup-status", h.SetupStatus)
	}
}

func registerAuthRoutes(g *gin.RouterGroup, h *handler.AuthHandler) {
	auth := g.Group("/auth")
	{
		// 无需登录
		auth.POST("/setup", h.Setup)
		auth.POST("/login", h.Login)

		// 需要登录
		me := auth.Group("")
		me.Use(middleware.RequireAuth())
		{
			me.POST("/logout", h.Logout)
			me.GET("/me", h.Me)
			me.PATCH("/me", h.UpdateMe)
			me.POST("/me/password", h.ChangePassword)
			me.POST("/me/last-subject", h.UpdateLastSubject)
		}
	}
}
