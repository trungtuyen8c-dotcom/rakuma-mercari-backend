package service

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

// Same rules as the frontend's guess.js: a guess is made only when it is unambiguous, otherwise the field stays
// empty for the owner to fill.

func normTitle(s string) string { return strings.ToLower(norm.NFKC.String(s)) }

// guessProduct returns the active product whose name or keyword appears in the title; the longest match wins and a
// tie between products gives none.
func guessProduct(title string, products []models.Product) (models.Product, bool) {
	t := normTitle(title)
	var best models.Product
	bestLen, tie := 0, false
	if t == "" {
		return best, false
	}
	for _, p := range products {
		if !p.Active {
			continue
		}
		for _, w := range append([]string{p.Name}, strings.Split(p.Keywords, ",")...) {
			w = strings.TrimSpace(normTitle(w))
			n := utf8.RuneCountInString(w)
			if n < 3 && !(n >= 2 && len(w) > n) || !strings.Contains(t, w) {
				continue
			}
			switch {
			case n > bestLen:
				best, bestLen, tie = p, n, false
			case n == bestLen && best.ID != p.ID:
				tie = true
			}
		}
	}
	return best, bestLen > 0 && !tie
}

var reBoxQty = regexp.MustCompile(`(^|[^\d.])(\d{1,2})(box|箱|ボックス)`)

// guessQty reads a quantity written next to a box word ("4BOX", "2箱"); "151 BOX" (a set name) is not one.
func guessQty(title string) int {
	m := reBoxQty.FindStringSubmatch(normTitle(title))
	if m == nil {
		return 0
	}
	q, _ := strconv.Atoi(m[2])
	if q < 2 || q > 50 {
		return 0
	}
	return q
}
