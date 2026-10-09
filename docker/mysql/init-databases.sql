-- 创建三个业务数据库并授予迁移、测试账号权限；建表由 cmd/migrate 的 Goose 执行。
-- Compose 通过 /docker-entrypoint-initdb.d 加载，其他环境可用 MySQL 客户端执行同一文件。
-- 缺少授权会使迁移报 Access denied。

CREATE DATABASE IF NOT EXISTS gateway_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS central_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS worker_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

GRANT ALL PRIVILEGES ON gateway_db.* TO 'chargepilot'@'%';
GRANT ALL PRIVILEGES ON central_db.* TO 'chargepilot'@'%';
GRANT ALL PRIVILEGES ON worker_db.* TO 'chargepilot'@'%';
FLUSH PRIVILEGES;
