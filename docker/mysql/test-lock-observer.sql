-- 仅供独立集成测试观察真实等待关系；生产和开发栈不加载此文件。
-- 不授予业务写权限、PROCESS 或全局权限，缺观测权限的测试必须失败。
GRANT SELECT ON performance_schema.data_lock_waits TO 'chargepilot'@'%';
GRANT SELECT ON performance_schema.data_locks TO 'chargepilot'@'%';
GRANT SELECT ON performance_schema.threads TO 'chargepilot'@'%';
