package service

import (
	"testing"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

func TestTrackingFromMessages(t *testing.T) {
	seller := func(b string) models.RakumaMessage { return models.RakumaMessage{From: "seller", Body: b} }
	cases := []struct {
		msgs              []models.RakumaMessage
		tracking, carrier string
	}{
		{[]models.RakumaMessage{seller("お世話になっております。 magi運営事務局です。\n本日ゆうパックにて発送いたしました。 追跡番号は194926646250です。")}, "194926646250", "ゆうパック"},
		{[]models.RakumaMessage{seller("お問い合わせ番号：１２３４－５６７８－９０１２ ヤマトで送りました")}, "123456789012", "ヤマト運輸"},
		{[]models.RakumaMessage{seller("Tracking number: 5432-1098-7654")}, "543210987654", ""},
		{[]models.RakumaMessage{seller("ご購入ありがとうございます。10月13日に発送予定です。")}, "", ""},                                        // a date, no label
		{[]models.RakumaMessage{seller("合計 24999円、注文番号 562650293001 です")}, "", ""},                                      // order number is not a tracking label
		{[]models.RakumaMessage{{From: "buyer", Body: "追跡番号は111122223333ですか？"}}, "", ""},                                // the buyer's own message
		{[]models.RakumaMessage{seller("追跡番号は111122223333です"), seller("訂正です。追跡番号は444455556666です")}, "444455556666", ""}, // newest wins
		{[]models.RakumaMessage{seller("追跡番号は12345です")}, "", ""},                                                        // too short
	}
	for i, c := range cases {
		tr, ca := trackingFromMessages(c.msgs)
		if tr != c.tracking || ca != c.carrier {
			t.Errorf("case %d: got %q %q, want %q %q", i, tr, ca, c.tracking, c.carrier)
		}
	}
}
