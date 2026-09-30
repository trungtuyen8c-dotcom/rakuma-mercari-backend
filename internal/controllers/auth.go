package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/middlewares"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

const stateCookie = "rakuma_oauth_state"

type AuthHandlers struct{ Svc *service.Service }

func (h *AuthHandlers) oauth() *oauth2.Config {
	cfg := h.Svc.Cfg
	return &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.AppURL + "/api/v1/auth/google/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

// Config tells the UI which sign-in methods exist.
func (h *AuthHandlers) Config(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"google": h.Svc.Cfg.GoogleEnabled(), "devLogin": h.Svc.Cfg.DevLoginEnabled()})
}

func (h *AuthHandlers) Me(c *gin.Context) {
	p := middlewares.GetPrincipal(c)
	if p == nil || p.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": service.ErrForbidden.Error()})
		return
	}
	c.JSON(http.StatusOK, p.User)
}

func (h *AuthHandlers) setSession(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middlewares.SessionCookie, token, int(service.SessionTTL.Seconds()), "/", "", h.Svc.Cfg.SecureCookies(), true)
}

func (h *AuthHandlers) Logout(c *gin.Context) {
	if tok, err := c.Cookie(middlewares.SessionCookie); err == nil {
		_ = h.Svc.DeleteSession(c.Request.Context(), tok)
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middlewares.SessionCookie, "", -1, "/", "", h.Svc.Cfg.SecureCookies(), true)
	c.Status(http.StatusNoContent)
}

// DevLogin signs in by email without Google. Disabled when APP_ENV=production. Still owner-only.
func (h *AuthHandlers) DevLogin(c *gin.Context) {
	if !h.Svc.Cfg.DevLoginEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": service.ErrNotFound.Error()})
		return
	}
	var in struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !bind(c, &in) {
		return
	}
	tok, err := h.Svc.CreateSession(c.Request.Context(), in.Email, in.Name)
	if err != nil {
		fail(c, err)
		return
	}
	h.setSession(c, tok)
	u, _ := h.Svc.SessionUser(c.Request.Context(), tok)
	c.JSON(http.StatusOK, u)
}

func (h *AuthHandlers) GoogleLogin(c *gin.Context) {
	if !h.Svc.Cfg.GoogleEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "Chưa cấu hình đăng nhập Google (GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET)."})
		return
	}
	state := service.RandomState()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(stateCookie, state, 600, "/", "", h.Svc.Cfg.SecureCookies(), true)
	c.Redirect(http.StatusFound, h.oauth().AuthCodeURL(state, oauth2.SetAuthURLParam("prompt", "select_account")))
}

// GoogleCallback exchanges the code, reads the verified email from Google, and opens a session for the owner only.
func (h *AuthHandlers) GoogleCallback(c *gin.Context) {
	back := func(errMsg string) {
		target := h.Svc.Cfg.AppURL + "/"
		if errMsg != "" {
			target += "?auth_error=" + url.QueryEscape(errMsg)
		}
		c.Redirect(http.StatusFound, target)
	}
	state, err := c.Cookie(stateCookie)
	if err != nil || state == "" || state != c.Query("state") {
		back("Phiên đăng nhập hết hạn, vui lòng thử lại.")
		return
	}
	c.SetCookie(stateCookie, "", -1, "/", "", h.Svc.Cfg.SecureCookies(), true)
	cfg := h.oauth()
	tok, err := cfg.Exchange(c.Request.Context(), c.Query("code"))
	if err != nil {
		back("Không xác thực được với Google.")
		return
	}
	resp, err := cfg.Client(c.Request.Context(), tok).Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		back("Không xác thực được với Google.")
		return
	}
	defer resp.Body.Close()
	var info struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || !info.EmailVerified {
		back("Email Google chưa được xác minh.")
		return
	}
	session, err := h.Svc.CreateSession(c.Request.Context(), info.Email, info.Name)
	if err != nil {
		var fe *service.ForbiddenError
		if errors.As(err, &fe) {
			back(fe.Msg)
			return
		}
		back("Chưa đăng nhập được, vui lòng thử lại.")
		return
	}
	h.setSession(c, session)
	back("")
}
