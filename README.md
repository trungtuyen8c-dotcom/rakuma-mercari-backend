# rakuma-mercari-backend

Go + Gin API for the Rakuma/Mercari purchase, sales, stock and profit system. Business rules follow `docs/QT-01-he-thong-quan-ly-mua-ban-rakuma.md` (BR-xx, E-xx, AC-xx).

- PostgreSQL. Migrations are embedded and applied on start (`migrations/`). Stock and totals are SQL views (`stock_by_period`, `period_totals`); purchase and sale totals are generated columns.
- Every write runs in a transaction with an `audit_logs` row. Closed periods are read-only.
- Auth: Google OAuth, and only `RAKUMA_OWNER_EMAIL` gets a session. Without `GOOGLE_CLIENT_ID`, dev login is used instead, and it is refused when `APP_ENV=production`. API keys (`Authorization: Bearer rk_live_...`) are `read` or `write`.

## Commands

Go is not required locally. Everything runs in Docker.

```sh
./scripts/test.sh            # go vet + all tests against a throwaway Postgres
./scripts/test.sh -v -run AC # pass extra go test flags
```

Run the full stack from `../rakuma-mercari-infra` (`make up`).

Binaries in the image:
- `server`: the HTTP API on `:8080`.
- `import-excel [-inspect] rakuma_t7.xlsx`: imports into an empty DB. It rolls back unless total cost is 69,824,506¥, revenue 69,815,821¥ and stock 560.
- `seed-demo`: loads the UI prototype's sample data into an empty DB. Dev only.

## API (`/api/v1`)

| Access | Routes |
|---|---|
| read (session or any key) | `GET /products /purchases /sales /stock /dashboard /periods /analysis/products/:id` |
| write (session or write key) | `POST /products /purchases /sales` (add `?force=true` to accept warnings) |
| owner session only | `GET /state`, `PATCH/DELETE /products/:id /purchases/:id`, `DELETE /sales/:id`, `PUT /stock/:product_id/opening`, `POST /periods/close`, `GET/PUT /settings`, `GET/POST /api-keys`, `POST /api-keys/:id/revoke` |
| public | `GET /auth/config /auth/me`, `POST /auth/logout /auth/dev-login`, `GET /auth/google/login /auth/google/callback`, `GET /healthz` |

Errors: `422 {errors}` for invalid fields, `409 {warnings}` for soft warnings (retry with `?force=true`), and `409/403/404 {error}` otherwise.

## Excel import: columns to confirm

Spec §15.1 confirms purchase columns C–I, sale columns C–G, catalog B, stock B/C, and settings B9/B10. The spec does not pin down the purchase date, check, review and note columns, or the sale date and customer columns. Defaults are B, J, K, L, B, H. Run `import-excel -inspect` first and override them with flags if needed.
