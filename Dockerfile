FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /bin/ytbloader cmd/server/main.go

FROM alpine:latest

RUN apk --no-cache add ca-certificates yt-dlp

WORKDIR /root/

COPY --from=builder /bin/ytbloader .
COPY .env.example .env

EXPOSE 8080

CMD ["./ytbloader"]