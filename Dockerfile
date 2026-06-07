FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o simple-finance ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/simple-finance .
COPY --from=builder /app/web ./web
COPY --from=builder /app/migrations ./migrations
EXPOSE 8080
CMD ["./simple-finance"]
