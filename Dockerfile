FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -ldflags="-s -w" -o bot-news ./cmd/bot-news

# ---

FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata wget \
    && adduser -D -s /bin/sh appuser

WORKDIR /app

COPY --from=builder /app/bot-news .

RUN chown -R appuser:appuser /app

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./bot-news"]
