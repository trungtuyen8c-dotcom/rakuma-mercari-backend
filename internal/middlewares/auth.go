package middlewares

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

const (
	SessionCookie = "rakuma_session"
	principalKey  = "principal"
)

// Authenticate resolves the caller from the session cookie or an "Authorization: Bearer rk_live_..." API key.
// It never rejects; the Require* middlewares do.
func Authenticate(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if p, err := svc.APIKeyPrincipal(c.Request.Context(), strings.TrimPrefix(h, "Bearer ")); err == nil {
				c.Set(principalKey, p)
			}
		} else if tok, err := c.Cookie(SessionCookie); err == nil && tok != "" {
			if u, err := svc.SessionUser(c.Request.Context(), tok); err == nil {
				c.Set(principalKey, &service.Principal{Actor: u.Email, Scope: "owner", User: u})
			}
		}
		c.Next()
	}
}

func GetPrincipal(c *gin.Context) *service.Principal {
	if v, ok := c.Get(principalKey); ok {
		return v.(*service.Principal)
	}
	return nil
}

func deny(c *gin.Context, status int) {
	c.AbortWithStatusJSON(status, gin.H{"error": service.ErrForbidden.Error()})
}

// RequireScope allows the owner session plus API keys whose scope is in the list (AC-04: enforced server-side).
func RequireScope(scopes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := GetPrincipal(c)
		if p == nil {
			deny(c, http.StatusUnauthorized)
			return
		}
		if p.Scope == "owner" {
			c.Next()
			return
		}
		for _, s := range scopes {
			if p.Scope == s {
				c.Next()
				return
			}
		}
		deny(c, http.StatusForbidden)
	}
}

// RequireOwner allows only the signed-in owner (no API keys).
func RequireOwner() gin.HandlerFunc { return RequireScope() }
