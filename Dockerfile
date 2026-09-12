FROM node:22-alpine AS frontend
WORKDIR /build/frontend
RUN corepack enable && corepack prepare pnpm@10.12.3 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build

FROM golang:1.26-alpine AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S audit && adduser -S -G audit audit
WORKDIR /app
COPY --from=backend /server /app/server
COPY --from=frontend /build/frontend/dist /app/frontend/dist
USER audit
ENV LISTEN_ADDR=0.0.0.0:8090 STATIC_DIR=/app/frontend/dist
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s CMD wget -q -O /dev/null http://127.0.0.1:8090/readyz || exit 1
ENTRYPOINT ["/app/server"]
