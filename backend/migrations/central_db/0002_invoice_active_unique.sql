-- +goose Up

-- 发票申请「一单一申请」改由数据库保证。
-- 此前该约束只靠订单行锁加应用层 COUNT 判重（settlement/user_invoice_repo.go），存在并发窗口。
-- 发票没有软删除业务路径：deleted_at 列存在但全仓无任何读写，因此唯一键覆盖全部行。
-- biz_type 为 ENUM，按 Q4 的分治规则其取值以本迁移的列定义为准，文档不复述。
ALTER TABLE `invoice_request`
  ADD UNIQUE KEY `uk_invoice_active` (`biz_type`, `biz_id`);

-- +goose Down

ALTER TABLE `invoice_request`
  DROP INDEX `uk_invoice_active`;
