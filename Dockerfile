FROM golang:1.23.0-alpine3.20 AS builder

WORKDIR /app
COPY . .

RUN go mod download
RUN CURRENT_DATETIME=$(date '+%Y-%m-%dT%H:%M:%S') && \
    CGO_ENABLED=0 go build -ldflags "-X main.BuildTime=${CURRENT_DATETIME}" -o collagify-tg ./cmd

FROM alpine:3.20

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/collagify-tg .
CMD ["./collagify-tg"]
