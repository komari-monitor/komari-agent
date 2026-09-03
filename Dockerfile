FROM alpine:3.21

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

COPY --chmod=755 komari-agent-${TARGETOS}-${TARGETARCH} /app/komari-agent

RUN touch /.komari-agent-container

ENTRYPOINT ["/app/komari-agent"]
# 运行时请指定参数
# Please specify parameters at runtime.
# eg: docker run komari-agent -e example.com -t token
CMD ["--help"]
