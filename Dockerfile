FROM node:24-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/server ./cmd/server
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 mediashelf && adduser -D -u 10001 -G mediashelf mediashelf && mkdir /data && chown mediashelf:mediashelf /data
COPY --from=backend /server /app/server
COPY --from=web /web/dist /app/web
ENV MEDIASHELF_ADDR=0.0.0.0:8765 MEDIASHELF_DATA_DIR=/data MEDIASHELF_WEB_DIR=/app/web
USER mediashelf
EXPOSE 8765
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["/app/server", "--healthcheck"]
ENTRYPOINT ["/app/server"]
