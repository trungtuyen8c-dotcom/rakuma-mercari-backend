package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/middlewares"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

// Handlers are thin: bind input, call the service, write JSON.
type Handlers struct{ Svc *service.Service }

func (h *Handlers) State(c *gin.Context) {
	st, err := h.Svc.State(c.Request.Context(), middlewares.GetPrincipal(c).User)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// Products

func (h *Handlers) ListProducts(c *gin.Context) {
	out, err := repo.ListProducts(c.Request.Context(), h.Svc.Pool)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) CreateProduct(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.AddProduct(c.Request.Context(), actor(c), in.Name)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) UpdateProduct(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in service.ProductPatch
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.UpdateProduct(c.Request.Context(), actor(c), id, in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) DeleteProduct(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	if err := h.Svc.DeleteProduct(c.Request.Context(), actor(c), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Purchases

func (h *Handlers) ListPurchases(c *gin.Context) {
	out, err := repo.ListPurchases(c.Request.Context(), h.Svc.Pool, periodQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) CreatePurchase(c *gin.Context) {
	var in service.PurchaseInput
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.AddPurchase(c.Request.Context(), actor(c), in, force(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) UpdatePurchase(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Checked  *bool   `json:"checked"`
		Reviewed *bool   `json:"reviewed"`
		Tracking *string `json:"tracking"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SetPurchaseFlags(c.Request.Context(), actor(c), id, in.Checked, in.Reviewed, in.Tracking)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) DeletePurchase(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	if err := h.Svc.DeletePurchase(c.Request.Context(), actor(c), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Sales

func (h *Handlers) ListSales(c *gin.Context) {
	out, err := repo.ListSales(c.Request.Context(), h.Svc.Pool, periodQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) CreateSale(c *gin.Context) {
	var in service.SaleInput
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.AddSale(c.Request.Context(), actor(c), in, force(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) DeleteSale(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	if err := h.Svc.DeleteSale(c.Request.Context(), actor(c), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Stock, reports, periods

func (h *Handlers) Stock(c *gin.Context) {
	p, rows, err := h.Svc.Stock(c.Request.Context(), periodQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"period": p, "rows": rows})
}

func (h *Handlers) SetOpening(c *gin.Context) {
	id, ok := idParam(c, "product_id")
	if !ok {
		return
	}
	var in struct {
		Qty service.Flex `json:"qty"`
	}
	if !bind(c, &in) {
		return
	}
	if err := h.Svc.SetOpening(c.Request.Context(), actor(c), id, in.Qty); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handlers) Dashboard(c *gin.Context) {
	out, err := h.Svc.Dashboard(c.Request.Context(), periodQuery(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) Analysis(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	out, err := h.Svc.Analysis(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) ListPeriods(c *gin.Context) {
	out, err := repo.ListPeriods(c.Request.Context(), h.Svc.Pool)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) ClosePeriod(c *gin.Context) {
	out, err := h.Svc.ClosePeriod(c.Request.Context(), actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Settings and API keys

func (h *Handlers) GetSettings(c *gin.Context) {
	out, err := h.Svc.Settings(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) SaveSettings(c *gin.Context) {
	var in service.SettingsInput
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SaveSettings(c.Request.Context(), actor(c), in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) ListAPIKeys(c *gin.Context) {
	out, err := repo.ListAPIKeys(c.Request.Context(), h.Svc.Pool)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) CreateAPIKey(c *gin.Context) {
	var in struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if !bind(c, &in) {
		return
	}
	rec, key, err := h.Svc.CreateAPIKey(c.Request.Context(), actor(c), in.Name, in.Scope)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"key": key, "record": rec})
}

func (h *Handlers) RevokeAPIKey(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	out, err := h.Svc.RevokeAPIKey(c.Request.Context(), actor(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Rakuma sync

func (h *Handlers) ListRakuma(c *gin.Context) {
	out, err := repo.ListRakumaOrders(c.Request.Context(), h.Svc.Pool)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) SyncRakuma(c *gin.Context) {
	var in struct {
		Orders []service.RakumaOrderInput `json:"orders"`
		Listed []string                   `json:"listedLinks"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SyncRakuma(c.Request.Context(), actor(c), in.Orders, in.Listed)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) ApproveRakuma(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in service.PurchaseInput
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.ApproveRakuma(c.Request.Context(), actor(c), id, in, force(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) DismissRakuma(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Dismissed bool `json:"dismissed"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.DismissRakuma(c.Request.Context(), actor(c), id, in.Dismissed)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) HandleRakumaMessages(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	out, err := h.Svc.HandleRakumaMessages(c.Request.Context(), actor(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) UpdateRakuma(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Rating    *string `json:"rating"`
		IssueNote *string `json:"issueNote"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.UpdateRakumaNotes(c.Request.Context(), actor(c), id, in.Rating, in.IssueNote)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) AddRakumaReply(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.AddRakumaReply(c.Request.Context(), actor(c), id, in.Body)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) SetRakumaReplyTranslation(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		BodyJa string `json:"bodyJa"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SetRakumaReplyTranslation(c.Request.Context(), actor(c), id, in.BodyJa)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) MarkRakumaReplySent(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		BodyJa string `json:"bodyJa"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.MarkRakumaReplySent(c.Request.Context(), actor(c), id, in.BodyJa)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) DeleteRakumaReply(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	out, err := h.Svc.DeleteRakumaReply(c.Request.Context(), actor(c), id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) BroadcastRakuma(c *gin.Context) {
	var in struct {
		OrderIDs []string `json:"orderIds"`
		Body     string   `json:"body"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.BroadcastRakuma(c.Request.Context(), actor(c), in.OrderIDs, in.Body)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) SkipRakumaReply(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Reason     string `json:"reason"`
		ChatClosed bool   `json:"chatClosed"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SkipRakumaReply(c.Request.Context(), actor(c), id, in.Reason, in.ChatClosed)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) OpenNextPeriod(c *gin.Context) {
	var in struct {
		Start string `json:"start"`
	}
	if c.Request.ContentLength > 0 && !bind(c, &in) {
		return
	}
	out, err := h.Svc.OpenNextPeriod(c.Request.Context(), actor(c), in.Start)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *Handlers) EditPurchase(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in service.PurchaseInput
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.UpdatePurchase(c.Request.Context(), actor(c), id, in, force(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) EditSale(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in service.SaleInput
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.UpdateSale(c.Request.Context(), actor(c), id, in, force(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) SetStock(c *gin.Context) {
	id, ok := idParam(c, "product_id")
	if !ok {
		return
	}
	var in struct {
		Qty service.Flex `json:"qty"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SetStock(c.Request.Context(), actor(c), periodQuery(c), id, in.Qty)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handlers) SetPeriodTotals(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		TotalCost    service.Flex `json:"totalCost"`
		TotalRevenue service.Flex `json:"totalRevenue"`
	}
	if !bind(c, &in) {
		return
	}
	out, err := h.Svc.SetPeriodTotals(c.Request.Context(), actor(c), id, in.TotalCost, in.TotalRevenue)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
