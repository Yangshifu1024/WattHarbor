# 镜像内置 Node.js 和 MySQL 客户端，宿主机无需安装数据库客户端。
FROM node:24-bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends default-mysql-client \
    && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["node", "/provision-runtime.mjs"]
