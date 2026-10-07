# 同一镜像提供 Node.js 与兼容 MySQL 的客户端，宿主机无需 shell 或数据库客户端。
FROM node:24-bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends default-mysql-client \
    && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["node", "/provision-runtime.mjs"]
