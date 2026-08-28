# Agent Matrix — 单二进制 + 嵌入式 SQLite，容器化同样零外部依赖
# 构建：docker build -t agent-matrix .
# 运行：docker run -d -p 26817:26817 -v am-data:/data \
#         -e AGENT_MATRIX_BASE_URL=https://matrix.example.com agent-matrix

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY setup.sh ./
COPY web ./web
# CGO 关闭 + 静态链接：产物拷进任意精简基础镜像都能直接跑
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agent-matrix .

FROM alpine:3.20
# 预建 /data 并归属运行用户：VOLUME 匿名卷首用会继承镜像内目录属主；
# bind mount 时需宿主目录对 uid 10001 可写（compose 示例已说明）
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 -g agent-matrix am \
    && mkdir -p /data && chown am:am /data
COPY --from=build /out/agent-matrix /usr/local/bin/agent-matrix
USER am
EXPOSE 26817
# 数据库与附件都落在 /data（AGENT_MATRIX_DB 默认 /data/agent-matrix.db，
# 附件目录随 DB 目录），持久化只需挂这一个卷
ENV AGENT_MATRIX_DB=/data/agent-matrix.db
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:26817/healthz | grep -q '"ok":true' || exit 1
ENTRYPOINT ["/usr/local/bin/agent-matrix"]
