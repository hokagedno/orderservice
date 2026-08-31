# Один Dockerfile на оба сервиса: нужный бинарник выбирается
# аргументом сборки SERVICE (orderapi или inventory).
FROM golang:1.25-alpine AS builder

ARG SERVICE=orderapi
WORKDIR /src

# Слой с зависимостями кэшируется отдельно от исходников.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/app ./cmd/${SERVICE}

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget
RUN adduser -D -u 10001 appuser
USER appuser

COPY --from=builder /out/app /usr/local/bin/app

ENTRYPOINT ["/usr/local/bin/app"]
