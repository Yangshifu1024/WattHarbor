-- 建三个业务 schema 并把权限授给应用账号。建表 DDL 归 cmd/migrate 里的 Goose 管，
-- 这里只管库与权限。
--
-- 一份 SQL 两个调用方，别各自复制：
--   1. compose：挂进 /docker-entrypoint-initdb.d（见 docker-compose.yml）。
--   2. CI（.github/workflows/ci.yml）：MySQL 是 service 容器，runner 上未必装了
--      mysql 客户端，所以用 mysql 镜像自带的客户端连过去执行。
--
-- 少 GRANT 会在迁移第一步就报 Access denied，而那条报错完全指不到授权这一层。

CREATE DATABASE IF NOT EXISTS gateway_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS central_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS worker_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

GRANT ALL PRIVILEGES ON gateway_db.* TO 'chargepilot'@'%';
GRANT ALL PRIVILEGES ON central_db.* TO 'chargepilot'@'%';
GRANT ALL PRIVILEGES ON worker_db.* TO 'chargepilot'@'%';
FLUSH PRIVILEGES;
