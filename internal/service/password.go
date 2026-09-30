package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/repo"
)

const (
	MinPasswordLen = 10
	maxFails       = 5
	lockFor        = 15 * time.Minute
	bcryptCost     = 12
)

// AuthError is a failed sign-in: 401 for wrong credentials, 429 while locked out.
type AuthError struct {
	Msg    string
	Status int
}

func (e *AuthError) Error() string { return e.Msg }

var errBadLogin = &AuthError{Msg: "Email hoặc mật khẩu không đúng.", Status: 401}

// dummyHash keeps the response time the same whether or not the account exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("rakuma-timing-equalizer"), bcryptCost)

// limiter locks a client IP after maxFails wrong passwords, for lockFor.
type limiter struct {
	mu    sync.Mutex
	fails map[string]int
	until map[string]time.Time
}

var loginLimiter = &limiter{fails: map[string]int{}, until: map[string]time.Time{}}

func (l *limiter) locked(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.until[key]; ok {
		if now.Before(t) {
			return t.Sub(now)
		}
		delete(l.until, key)
		delete(l.fails, key)
	}
	return 0
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key]++
	if l.fails[key] >= maxFails {
		l.until[key] = now.Add(lockFor)
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
	delete(l.until, key)
}

func validatePassword(pw string) string {
	if len([]rune(pw)) < MinPasswordLen {
		return fmt.Sprintf("Mật khẩu phải có ít nhất %d ký tự.", MinPasswordLen)
	}
	if len(pw) > 72 { // bcrypt limit
		return "Mật khẩu tối đa 72 ký tự."
	}
	return ""
}

// PasswordEnabled reports whether the owner has a password set.
func (s *Service) PasswordEnabled(ctx context.Context) bool {
	ok, err := repo.HasPassword(ctx, s.Pool, s.Cfg.OwnerEmail)
	return err == nil && ok
}

// SetPassword stores a bcrypt hash for the owner (used by the set-password command).
func (s *Service) SetPassword(ctx context.Context, email, pw string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email != s.Cfg.OwnerEmail {
		return &ValidationError{Msg: "Chỉ đặt mật khẩu cho email chủ shop (" + s.Cfg.OwnerEmail + ")."}
	}
	if m := validatePassword(pw); m != "" {
		return &ValidationError{Fields: map[string]string{"password": m}}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := repo.SetPasswordHash(ctx, tx, email, string(hash)); err != nil {
			return err
		}
		return repo.Audit(ctx, tx, "user", email, "set_password", email, nil, nil)
	})
}

// PasswordLogin checks email + password for the owner and returns a session token.
func (s *Service) PasswordLogin(ctx context.Context, clientIP, email, pw string) (string, error) {
	now := s.Now()
	if wait := loginLimiter.locked(clientIP, now); wait > 0 {
		mins := int(math.Ceil(wait.Minutes()))
		return "", &AuthError{Msg: fmt.Sprintf("Đăng nhập sai quá nhiều lần. Thử lại sau %d phút.", mins), Status: 429}
	}
	email = strings.ToLower(strings.TrimSpace(email))
	hash, err := repo.PasswordHash(ctx, s.Pool, email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if hash == "" || email != s.Cfg.OwnerEmail {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		loginLimiter.fail(clientIP, now)
		return "", errBadLogin
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) != nil {
		loginLimiter.fail(clientIP, now)
		_ = repo.Audit(ctx, s.Pool, "session", email, "sign_in_failed", clientIP, nil, nil)
		return "", errBadLogin
	}
	loginLimiter.reset(clientIP)
	return s.CreateSession(ctx, email, "Chủ shop")
}

// ChangePassword requires the current password.
func (s *Service) ChangePassword(ctx context.Context, email, current, next string) error {
	hash, err := repo.PasswordHash(ctx, s.Pool, email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return &ValidationError{Fields: map[string]string{"current": "Mật khẩu hiện tại không đúng."}}
	}
	if current == next {
		return &ValidationError{Fields: map[string]string{"next": "Mật khẩu mới phải khác mật khẩu hiện tại."}}
	}
	if m := validatePassword(next); m != "" {
		return &ValidationError{Fields: map[string]string{"next": m}}
	}
	return s.SetPassword(ctx, email, next)
}
