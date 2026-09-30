package repo

import (
	"context"
	"strconv"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/models"
)

// Setting keys (spec §15.2). None of them feed any formula yet (BR-16, Q-08).
const (
	KeyExchangeRate  = "exchange_rate"
	KeyServiceFee    = "default_service_fee"
	KeyIntlShipping  = "default_intl_shipping"
	KeyImportTaxRate = "import_tax_rate"
)

func GetSettings(ctx context.Context, db DB) (models.Settings, error) {
	rows, err := db.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return models.Settings{}, err
	}
	defer rows.Close()
	var s models.Settings
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return s, err
		}
		switch k {
		case KeyExchangeRate:
			s.Rate, _ = strconv.ParseFloat(v, 64)
		case KeyServiceFee:
			s.ServiceFee, _ = strconv.ParseInt(v, 10, 64)
		case KeyIntlShipping:
			s.Shipping, _ = strconv.ParseInt(v, 10, 64)
		case KeyImportTaxRate:
			s.Tax, _ = strconv.ParseFloat(v, 64)
		}
	}
	return s, rows.Err()
}

func SetSetting(ctx context.Context, db DB, key, value string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}
