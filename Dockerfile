# 前端只在构建阶段需要 Node；固定工具版本，并按锁文件安装。
FROM node:24.22.3-bookworm-slim AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.26.5-bookworm AS backend
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /src/backend/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/web-ssh ./cmd/server

FROM debian:bookworm-slim AS runtime
WORKDIR /app
RUN mkdir -p /data && chown 10001:10001 /data /app
COPY --from=backend /out/web-ssh /app/web-ssh
USER 10001:10001
EXPOSE 8080
STOPSIGNAL SIGTERM
# exec 形式保证 Go 主进程直接收到停止信号；配置和密钥由部署方注入。
ENTRYPOINT ["/app/web-ssh"]
CMD ["-config", "/app/config.yaml"]
