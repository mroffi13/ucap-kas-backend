# Stage 0: Shared Go 1.27 base with cached module dependencies
FROM golang:1.27-alpine AS base
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata git

COPY go.mod go.sum ./
RUN go mod download

# Stage 1: Development stage with Air Hot-Reload
FROM base AS dev
RUN go install github.com/air-verse/air@latest
EXPOSE 8080
CMD ["air", "-c", ".air.toml"]

# Stage 2: Build static Go 1.27 binary for production
FROM base AS builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /ucapkas-api ./cmd/api

# Stage 3: Minimal non-root production runtime image (~20 MB, default target)
FROM alpine:3.20 AS prod
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S ucapkas && adduser -S ucapkas -G ucapkas

WORKDIR /app
COPY --from=builder /ucapkas-api /app/ucapkas-api

USER ucapkas
EXPOSE 8080
ENTRYPOINT ["/app/ucapkas-api"]
