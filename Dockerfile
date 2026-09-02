FROM golang:1.25-alpine AS builder
RUN apk --no-cache add gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# cgo is required by github.com/chai2010/webp; link statically for the alpine runtime
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags '-linkmode external -extldflags "-static"' -o shopsync .

FROM alpine:3.21
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/shopsync /shopsync
ENTRYPOINT ["/shopsync"]
