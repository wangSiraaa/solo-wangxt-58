// Package httpapi 提供只含 API 的 HTTP 层（无前端）。
package httpapi

import (
	"io"

	"github.com/gin-gonic/gin"

	"niche-service/internal/config"
	"niche-service/internal/service"
)

func NewRouter(svc *service.Service, tokens map[string]config.Identity, logOut io.Writer) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(RedactingLogger(logOut), gin.Recovery())

	h := &handlers{svc: svc}

	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	api := r.Group("/api/v1", Auth(tokens))
	{
		// 空位查询：窗口/复核/管理员
		api.GET("/niches/available", RequireRole("clerk", "reviewer", "admin"), h.listAvailableNiches)
		api.GET("/niches/:id", RequireRole("clerk", "reviewer", "admin"), h.getNiche)

		// 联系人：敏感信息，按角色限制并脱敏
		api.POST("/contacts", RequireRole("clerk", "admin"), h.createContact)
		api.GET("/contacts/:id", RequireRole("clerk", "reviewer", "admin"), h.getContact)

		// 合同
		api.POST("/contracts", RequireRole("clerk", "admin"), h.createContract)
		api.GET("/contracts/:id", RequireRole("clerk", "reviewer", "finance", "admin"), h.getContract)
		api.POST("/contracts/:id/contacts", RequireRole("clerk", "admin"), h.attachContact)
		api.POST("/contract-contacts/:id/verify", RequireRole("reviewer", "admin"), h.verifyContact)
		api.POST("/contracts/:id/review", RequireRole("reviewer", "admin"), h.reviewContract)
		api.POST("/contracts/:id/checkout", RequireRole("clerk", "admin"), h.checkout)

		// 缴费：开单走窗口，回执走财务；任何角色都不能用布尔值放行
		api.POST("/contracts/:id/payments", RequireRole("clerk", "admin"), h.createPayment)
		api.GET("/contracts/:id/payments", RequireRole("clerk", "finance", "admin"), h.listPayments)
		api.POST("/payments/:id/mock-receipt", RequireRole("finance", "admin"), h.mockReceipt)

		// 迁位
		api.POST("/contracts/:id/relocations", RequireRole("clerk", "admin"), h.createRelocation)
		api.POST("/relocations/:id/commit", RequireRole("clerk", "admin"), h.commitRelocation)
		api.GET("/relocations/:id", RequireRole("clerk", "reviewer", "admin"), h.getRelocation)
	}
	return r
}
