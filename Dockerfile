# syntax=docker/dockerfile:1

# ---------- 构建阶段 ----------
FROM golang:1.24-alpine AS builder

WORKDIR /src

# 先只拷贝依赖清单，利用层缓存：源码变动时不必重新下载依赖。
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# 构建参数，用于注入版本信息（CI 会传入 tag、commit、构建时间）。
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# CGO_ENABLED=0 产出静态链接二进制，才能放进 alpine 运行。
# -trimpath 去掉构建机路径，便于复现构建。
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w \
        -X codearts2api/internal/version.Version=${VERSION} \
        -X codearts2api/internal/version.Commit=${COMMIT} \
        -X codearts2api/internal/version.Date=${BUILD_DATE}" \
      -o /out/codearts2api .

# ---------- 运行阶段 ----------
FROM alpine:3.20

# ca-certificates 是与华为云 HTTPS 通信的必需品；
# tzdata 让签到状态里的「今天」按本地时区计算，否则默认 UTC 会与用户认知错位。
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -g 10001 -S app \
 && adduser -u 10001 -S app -G app

COPY --from=builder /out/codearts2api /usr/local/bin/codearts2api

# 数据目录：模型缓存与签到状态落在这里，便于以 volume 挂载持久化。
RUN mkdir -p /data && chown -R app:app /data

USER app
WORKDIR /data

ENV CODEARTS_MODEL_CACHE=/data/models-cache.json \
    CODEARTS_BENEFIT_STATE=/data/benefit-state.json \
    # 容器内必须监听 0.0.0.0，否则端口无法从宿主机访问。
    CODEARTS_PROXY_HOST=0.0.0.0 \
    CODEARTS_PROXY_PORT=8787 \
    TZ=Asia/Shanghai

EXPOSE 8787

# 用 -version 做健康检查：它不依赖 AK/SK，能验证二进制可正常执行。
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["codearts2api", "-version"]

ENTRYPOINT ["codearts2api"]
