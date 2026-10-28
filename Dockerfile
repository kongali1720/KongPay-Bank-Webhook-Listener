# Stage 1: Build binary Go dengan versi terbaru
FROM golang:alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o kongpay-app .

# Stage 2: Runtime image yang ringan
FROM alpine:latest

WORKDIR /app

# Salin binary dan sertifikat mTLS dari builder
COPY --from=builder /app/kongpay-app .
COPY --from=builder /app/ca.crt .
COPY --from=builder /app/server.crt .
COPY --from=builder /app/server.key .

EXPOSE 8443 9090

CMD ["./kongpay-app"]
