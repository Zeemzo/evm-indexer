# Build stage
FROM golang:1.24-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/indexer ./cmd/indexer

# Runtime stage: distroless-style minimal image, non-root.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 indexer
USER indexer

COPY --from=build /bin/indexer /usr/local/bin/indexer

EXPOSE 8080
ENTRYPOINT ["indexer"]
