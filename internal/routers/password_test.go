package routers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/routers"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

// login posts credentials from a given client IP (as nginx would set X-Real-IP).
func (e *env) login(ip, email, pw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"`+pw+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", ip)
	req.RemoteAddr = "172.18.0.5:40000" // nginx container
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestPasswordLogin(t *testing.T) {
	e := setup(t)
	svc := service.New(e.pool, config.Config{OwnerEmail: owner})
	ctx := context.Background()

	if err := svc.SetPassword(ctx, owner, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := svc.SetPassword(ctx, "other@example.com", "long-enough-pw"); err == nil {
		t.Fatal("non-owner password accepted")
	}
	if err := svc.SetPassword(ctx, owner, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if cfg := e.ok("GET", "/api/v1/auth/config", nil, 200); cfg["password"] != true {
		t.Fatalf("config=%v", cfg)
	}

	if res := e.login("203.0.113.1", owner, "wrong password!"); res.Code != 401 || !strings.Contains(res.Body.String(), "không đúng") {
		t.Fatalf("wrong password: %d %s", res.Code, res.Body)
	}
	if res := e.login("203.0.113.1", "other@example.com", "correct horse battery"); res.Code != 401 {
		t.Fatalf("non-owner: %d", res.Code)
	}
	res := e.login("203.0.113.1", "  OWNER@example.com ", "correct horse battery")
	if res.Code != 200 {
		t.Fatalf("login: %d %s", res.Code, res.Body)
	}
	var cookie string
	for _, c := range res.Result().Cookies() {
		if c.Name == "rakuma_session" {
			cookie = c.Value
			if !c.HttpOnly {
				t.Fatal("session cookie not HttpOnly")
			}
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	req.AddCookie(&http.Cookie{Name: "rakuma_session", Value: cookie})
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("session not usable: %d", rec.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	e := setup(t)
	svc := service.New(e.pool, config.Config{OwnerEmail: owner})
	if err := svc.SetPassword(context.Background(), owner, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	ip := "198.51.100.7"
	for i := range 5 {
		if res := e.login(ip, owner, "nope nope nope"); res.Code != 401 {
			t.Fatalf("attempt %d: %d", i+1, res.Code)
		}
	}
	// Locked: even the right password is refused from this IP
	if res := e.login(ip, owner, "correct horse battery"); res.Code != 429 {
		t.Fatalf("locked login: %d %s", res.Code, res.Body)
	}
	// A forged X-Forwarded-For does not change the client identity
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"`+owner+`","password":"correct horse battery"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", ip)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.RemoteAddr = "172.18.0.5:40000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("XFF bypass: %d", rec.Code)
	}
	// Another client is not affected
	if res := e.login("198.51.100.8", owner, "correct horse battery"); res.Code != 200 {
		t.Fatalf("other ip: %d", res.Code)
	}
}

func TestChangePassword(t *testing.T) {
	e := setup(t)
	svc := service.New(e.pool, config.Config{OwnerEmail: owner})
	if err := svc.SetPassword(context.Background(), owner, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	out := e.ok("PUT", "/api/v1/auth/password", map[string]string{"current": "bad", "next": "brand new password"}, 422)
	if out["errors"].(map[string]any)["current"] == nil {
		t.Fatalf("wrong current accepted: %v", out)
	}
	e.ok("PUT", "/api/v1/auth/password", map[string]string{"current": "correct horse battery", "next": "short"}, 422)
	e.ok("PUT", "/api/v1/auth/password", map[string]string{"current": "correct horse battery", "next": "brand new password"}, 204)
	if res := e.login("192.0.2.9", owner, "brand new password"); res.Code != 200 {
		t.Fatalf("new password: %d", res.Code)
	}
	if res := e.login("192.0.2.10", owner, "correct horse battery"); res.Code != 401 {
		t.Fatalf("old password still works: %d", res.Code)
	}
	// Anonymous callers cannot change it
	if res := e.do("PUT", "/api/v1/auth/password", map[string]string{"current": "x", "next": "yyyyyyyyyyyy"}, "none"); res.Code != 401 {
		t.Fatalf("anonymous change: %d", res.Code)
	}
}

func TestDevLoginOffInProduction(t *testing.T) {
	e := setup(t)
	h := routers.New(service.New(e.pool, config.Config{AppEnv: "production", OwnerEmail: owner}))
	req := httptest.NewRequest("POST", "/api/v1/auth/dev-login", strings.NewReader(`{"email":"`+owner+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("dev-login in production: %d", rec.Code)
	}
}
