package router

import (
	"github.com/gin-gonic/gin"
	"github.com/lp/campus-market/internal/handler"
)

// RegisterAppealRoutes registers student-facing appeal endpoints.
func RegisterAppealRoutes(g *gin.RouterGroup, h *handler.AppealHandler, auth, apiLimiter gin.HandlerFunc) {
	appeals := g.Group("/appeals", auth)
	{
		appeals.POST("", apiLimiter, h.Create)
		appeals.GET("/me", apiLimiter, h.ListMine)
		appeals.GET("/:id", apiLimiter, h.GetProgress)
	}
}

// RegisterAdminAppealRoutes registers admin-only appeal review endpoints.
func RegisterAdminAppealRoutes(g *gin.RouterGroup, h *handler.AppealHandler, auth, requireAdmin, apiLimiter gin.HandlerFunc) {
	adminAppeals := g.Group("/admin/appeals", auth, requireAdmin)
	{
		adminAppeals.GET("", apiLimiter, h.AdminList)
		adminAppeals.POST("/:id/review", apiLimiter, h.AdminReview)
	}
}
