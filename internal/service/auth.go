package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

const SessionTTL = 7 * 24 * time.Hour

func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func RandomState() string { return randomToken(24) }

// CreateSession signs in the owner only (GD-01, E1). Returns the raw cookie token; only its hash is stored.
func (s *Service) CreateSession(ctx context.Context, email, name string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email != s.Cfg.OwnerEmail {
		return "", &ForbiddenError{Msg: fmt.Sprintf("Tài khoản %s không có quyền truy cập. Chỉ chủ shop được đăng nhập.", email)}
	}
	token := randomToken(32)
	if err := repo.InsertSession(ctx, s.Pool, Hash(token), email, name, s.Now().Add(SessionTTL)); err != nil {
		return "", err
	}
	return token, repo.Audit(ctx, s.Pool, "session", email, "sign_in", email, nil, nil)
}

func (s *Service) SessionUser(ctx context.Context, token string) (*models.User, error) {
	u, err := repo.GetSession(ctx, s.Pool, Hash(token))
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	return repo.DeleteSession(ctx, s.Pool, Hash(token))
}

// Principal is who is calling: the owner via session, or a tool via API key.
type Principal struct {
	Actor string
	Scope string // "owner", "write" or "read"
	User  *models.User
}

var errBadKey = errors.New("invalid api key")

func (s *Service) APIKeyPrincipal(ctx context.Context, key string) (*Principal, error) {
	name, scope, err := repo.UseAPIKey(ctx, s.Pool, Hash(key))
	if err != nil {
		return nil, errBadKey
	}
	return &Principal{Actor: "api_key:" + name, Scope: scope}, nil
}

// CreateAPIKey returns the full key once; the database keeps only its hash and a masked form.
func (s *Service) CreateAPIKey(ctx context.Context, actor, name, scope string) (models.APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return models.APIKey{}, "", &ValidationError{Fields: map[string]string{"name": "Đặt tên để biết key dùng ở đâu, ví dụ “Claude Desktop MCP”."}}
	}
	if scope != "write" {
		scope = "read"
	}
	var out models.APIKey
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return out, "", err
	}
	key := "rk_live_" + hex.EncodeToString(b)
	masked := key[:12] + "…" + key[len(key)-4:]
	err := s.tx(ctx, func(tx pgx.Tx) error {
		taken, err := repo.ActiveAPIKeyNameTaken(ctx, tx, name)
		if err != nil {
			return err
		}
		if taken {
			return &ValidationError{Fields: map[string]string{"name": "Đã có key đang hoạt động với tên này."}}
		}
		id, err := repo.InsertAPIKey(ctx, tx, name, scope, Hash(key), masked)
		if err != nil {
			return err
		}
		out, err = repo.GetAPIKey(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "api_key", id, "create", actor, nil, out)
	})
	if err != nil {
		return out, "", err
	}
	return out, key, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, actor string, id int64) (models.APIKey, error) {
	var out models.APIKey
	err := s.tx(ctx, func(tx pgx.Tx) error {
		before, err := repo.GetAPIKey(ctx, tx, id)
		if err != nil {
			return notFound(err)
		}
		if err := repo.RevokeAPIKey(ctx, tx, id); err != nil {
			return err
		}
		out, err = repo.GetAPIKey(ctx, tx, id)
		if err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "api_key", id, "revoke", actor, before, out)
	})
	return out, err
}
