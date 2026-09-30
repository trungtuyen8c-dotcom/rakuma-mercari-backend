FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/import-excel ./cmd/import-excel \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed-demo ./cmd/seed-demo

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=build /out/ /usr/local/bin/
USER app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["server"]
