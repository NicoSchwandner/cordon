FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /cordon-server ./cmd/server/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /cordon-server /usr/local/bin/cordon-server
COPY migrations/ /app/migrations/

EXPOSE 8443
CMD ["cordon-server"]
