// Package service holds the business rules (spec §8 BR-xx, §6 E-xx). Every write runs in a transaction with an audit row.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
)

// User-facing messages come from spec §6 so the UI can show them as-is.
var (
	ErrForbidden = errors.New("Bạn không có quyền thực hiện thao tác này.") // E1
	ErrNotFound  = errors.New("Không tìm thấy dữ liệu, có thể đã bị xóa.")  // E2
	ErrLocked    = errors.New("Kỳ này đã chốt, không thể sửa.")             // E12
)

// ValidationError: the input is invalid; Fields maps form field -> message (E3, E4, E7...).
type ValidationError struct {
	Fields map[string]string
	Msg    string
}

func (e *ValidationError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	for _, m := range e.Fields {
		return m
	}
	return "invalid input"
}

// WarningError: the input is valid but needs confirmation; the client retries with force (E5, E6, E9).
type WarningError struct{ Warnings []string }

func (e *WarningError) Error() string { return strings.Join(e.Warnings, " ") }

// ForbiddenError: a 403 with a specific message; errors.Is(err, ErrForbidden) is true.
type ForbiddenError struct{ Msg string }

func (e *ForbiddenError) Error() string { return e.Msg }
func (e *ForbiddenError) Unwrap() error { return ErrForbidden }

// ConflictError: the action is not allowed in the current state (E8, E11).
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

type Service struct {
	Pool *pgxpool.Pool
	Cfg  config.Config
	Now  func() time.Time
}

func New(pool *pgxpool.Pool, cfg config.Config) *Service {
	return &Service{Pool: pool, Cfg: cfg, Now: time.Now}
}

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Flex accepts a JSON number or string, so web forms (strings) and API clients (numbers) share one validator.
type Flex string

func (f *Flex) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = Flex(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = Flex(n.String())
	return nil
}

func (f Flex) empty() bool { return strings.TrimSpace(string(f)) == "" }

func (f Flex) int() (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(string(f)), 10, 64)
	return v, err == nil
}

func (f Flex) float() (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(string(f)), 64)
	return v, err == nil
}

func parseID(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v, err == nil && v > 0
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
