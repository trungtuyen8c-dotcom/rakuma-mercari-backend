#!/bin/sh
# Runs vet + tests in Docker against a throwaway Postgres. Needs only Docker.
set -eu
cd "$(dirname "$0")/.."

NET=rakuma-test-net
DB=rakuma-test-db
docker network create "$NET" >/dev/null 2>&1 || true
docker rm -f "$DB" >/dev/null 2>&1 || true
docker run -d --name "$DB" --network "$NET" -e POSTGRES_USER=rakuma -e POSTGRES_PASSWORD=rakuma -e POSTGRES_DB=rakuma_test postgres:17-alpine >/dev/null
trap 'docker rm -f "$DB" >/dev/null 2>&1 || true' EXIT
until docker exec "$DB" pg_isready -U rakuma -d rakuma_test >/dev/null 2>&1; do sleep 1; done

docker run --rm --network "$NET" \
  -v "$PWD":/src -v rakuma-gomod:/go/pkg/mod -v rakuma-gocache:/root/.cache/go-build -w /src \
  -e TEST_DATABASE_URL="postgres://rakuma:rakuma@$DB:5432/rakuma_test?sslmode=disable" \
  golang:1.26-alpine sh -c 'go vet ./... && go test -p 1 -count=1 ./... '"$*"
