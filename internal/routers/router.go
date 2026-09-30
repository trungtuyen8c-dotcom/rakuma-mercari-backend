package routers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/controllers"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/middlewares"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

// New wires routes. Access levels:
//
//	read  = owner session or any API key      (GET reports and lists)
//	write = owner session or "write" API key  (add products, purchases, sales)
//	owner = owner session only                (edit/delete, stock opening, close period, settings, API keys)
func New(svc *service.Service) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	h := &controllers.Handlers{Svc: svc}
	a := &controllers.AuthHandlers{Svc: svc}

	api := r.Group("/api/v1", middlewares.Authenticate(svc))

	auth := api.Group("/auth")
	auth.GET("/config", a.Config)
	auth.GET("/me", a.Me)
	auth.POST("/logout", a.Logout)
	auth.POST("/dev-login", a.DevLogin)
	auth.GET("/google/login", a.GoogleLogin)
	auth.GET("/google/callback", a.GoogleCallback)

	read := api.Group("", middlewares.RequireScope("read", "write"))
	read.GET("/products", h.ListProducts)
	read.GET("/purchases", h.ListPurchases)
	read.GET("/sales", h.ListSales)
	read.GET("/stock", h.Stock)
	read.GET("/dashboard", h.Dashboard)
	read.GET("/periods", h.ListPeriods)
	read.GET("/analysis/products/:id", h.Analysis)

	write := api.Group("", middlewares.RequireScope("write"))
	write.POST("/products", h.CreateProduct)
	write.POST("/purchases", h.CreatePurchase)
	write.POST("/sales", h.CreateSale)

	owner := api.Group("", middlewares.RequireOwner())
	owner.GET("/state", h.State)
	owner.PATCH("/products/:id", h.UpdateProduct)
	owner.DELETE("/products/:id", h.DeleteProduct)
	owner.PATCH("/purchases/:id", h.UpdatePurchase)
	owner.DELETE("/purchases/:id", h.DeletePurchase)
	owner.DELETE("/sales/:id", h.DeleteSale)
	owner.PUT("/stock/:product_id/opening", h.SetOpening)
	owner.POST("/periods/close", h.ClosePeriod)
	owner.GET("/settings", h.GetSettings)
	owner.PUT("/settings", h.SaveSettings)
	owner.GET("/api-keys", h.ListAPIKeys)
	owner.POST("/api-keys", h.CreateAPIKey)
	owner.POST("/api-keys/:id/revoke", h.RevokeAPIKey)

	return r
}
