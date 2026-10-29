FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/catalog-server ./cmd/server

FROM alpine:3.20
RUN addgroup -S catalog && adduser -S -G catalog -u 10001 catalog
WORKDIR /app
COPY --from=build /out/catalog-server /app/catalog-server
RUN mkdir -p /data && chown catalog:catalog /data
USER catalog
ENV PORT=8080 DATA_DIR=/data
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/app/catalog-server"]
