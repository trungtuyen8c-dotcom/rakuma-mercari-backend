package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/middlewares"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

// fail maps service errors to HTTP. Bodies:
//
//	422 {"errors": {field: msg}} | 409 {"warnings": [...]} (retry with ?force=true) | 409/403/404 {"error": msg}
func fail(c *gin.Context, err error) {
	var ve *service.ValidationError
	var we *service.WarningError
	var ce *service.ConflictError
	var fe *service.ForbiddenError
	var ae *service.AuthError
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &ve):
		if len(ve.Fields) > 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"errors": ve.Fields, "error": ve.Error()})
		} else {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": ve.Error()})
		}
	case errors.As(err, &we):
		c.JSON(http.StatusConflict, gin.H{"warnings": we.Warnings})
	case errors.As(err, &ce):
		c.JSON(http.StatusConflict, gin.H{"error": ce.Msg})
	case errors.As(err, &ae):
		c.JSON(ae.Status, gin.H{"error": ae.Msg})
	case errors.As(err, &fe):
		c.JSON(http.StatusForbidden, gin.H{"error": fe.Msg})
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrLocked):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.As(err, &pe) && pe.Code == "23505": // unique violation raced past the pre-check
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Dữ liệu bị trùng.", "detail": pe.ConstraintName})
	default:
		slog.Error("request failed", "path", c.FullPath(), "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Chưa lưu được, vui lòng thử lại."}) // E13
	}
}

func actor(c *gin.Context) string {
	if p := middlewares.GetPrincipal(c); p != nil {
		return p.Actor
	}
	return "anonymous"
}

func idParam(c *gin.Context, name string) (int64, bool) {
	v, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || v <= 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": service.ErrNotFound.Error()})
		return 0, false
	}
	return v, true
}

// periodQuery reads ?period_id=; 0 means the open period (or all, depending on the endpoint).
func periodQuery(c *gin.Context) int64 {
	v, _ := strconv.ParseInt(c.Query("period_id"), 10, 64)
	return v
}

func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu gửi lên không đúng định dạng."})
		return false
	}
	return true
}

func force(c *gin.Context) bool { return c.Query("force") == "true" }
