package service

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

// A seller who ships without らくまパック writes the number in the chat: "追跡番号は194926646250です",
// "お問い合わせ番号：1234-5678-9012". Only a number right after such a label counts, never a bare number.
var (
	reTrackingMsg = regexp.MustCompile(`(?i)(?:追跡番号|追跡ナンバー|お問い?合わ?せ番号|問い?合わ?せ番号|伝票番号|送り状番号|トラッキング番号|tracking\s*(?:no\.?|number|#)?)` +
		`[^0-9a-z]{0,8}?([0-9][0-9-]{8,17}[0-9])`)
	carriers = []struct{ word, name string }{
		{"ゆうパケット", "ゆうパケット"}, {"ゆうパック", "ゆうパック"}, {"クリックポスト", "クリックポスト"}, {"レターパック", "レターパック"},
		{"ネコポス", "ネコポス"}, {"宅急便", "ヤマト運輸"}, {"ヤマト", "ヤマト運輸"}, {"佐川", "佐川急便"}, {"日本郵便", "日本郵便"},
	}
)

// shippedStatus: the seller has sent the goods (or they arrived), so a tracking number is expected.
func shippedStatus(s string) bool {
	return strings.Contains(s, "発送済み") || strings.Contains(s, "配達")
}

// trackingFromMessages returns the tracking number (digits only, 10 to 14 of them) and carrier from the newest seller
// message that labels one, or "" when none does.
func trackingFromMessages(msgs []models.RakumaMessage) (tracking, carrier string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].From != "seller" {
			continue
		}
		body := norm.NFKC.String(msgs[i].Body)
		for _, m := range reTrackingMsg.FindAllStringSubmatch(body, -1) {
			digits := strings.ReplaceAll(m[1], "-", "")
			if len(digits) < 10 || len(digits) > 14 {
				continue
			}
			for _, c := range carriers {
				if strings.Contains(body, c.word) {
					carrier = c.name
					break
				}
			}
			return digits, carrier
		}
	}
	return "", ""
}
