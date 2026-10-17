FROM golang:1.24-alpine AS builder

RUN apk --no-cache add gcc musl-dev

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /bin/ytbloader cmd/server/main.go

FROM alpine:latest

ARG YTDLP_VERSION=2026.08.19
ARG YTDLP_SHA256=1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6

RUN apk --no-cache add ca-certificates python3 ffmpeg \
    && wget -qO /usr/local/bin/yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp" \
    && echo "${YTDLP_SHA256}  /usr/local/bin/yt-dlp" | sha256sum -c - \
    && chmod +x /usr/local/bin/yt-dlp

WORKDIR /root/

COPY --from=builder /bin/ytbloader .

EXPOSE 8070

CMD ["./ytbloader"]