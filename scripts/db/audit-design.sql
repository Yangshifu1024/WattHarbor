-- 同一只读一致性快照检查资金、来源关联和跨月业务号；输出类别与数量，不返回用户资料。
SET SESSION time_zone='+00:00';
SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ;
START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY;
SELECT 'wallet_invalid_amount',COUNT(*) FROM central_db.wallet_account
 WHERE balance_cents<0 OR frozen_cents<0 OR frozen_cents>balance_cents
UNION ALL SELECT 'wallet_balance_vs_ledger',COUNT(*) FROM central_db.wallet_account w
 LEFT JOIN (SELECT wallet_account_id,SUM(IF(direction='in',amount_cents,-amount_cents)) AS net FROM central_db.wallet_txn GROUP BY wallet_account_id) t ON t.wallet_account_id=w.id
 WHERE w.balance_cents<>COALESCE(t.net,0)
UNION ALL SELECT 'wallet_frozen_vs_parts',COUNT(*) FROM central_db.wallet_account w
 LEFT JOIN (SELECT wallet_account_id,SUM(amount_cents) AS reserved FROM central_db.wallet_refund_part WHERE settled=0 GROUP BY wallet_account_id) p ON p.wallet_account_id=w.id
 WHERE w.frozen_cents<>COALESCE(p.reserved,0)
UNION ALL SELECT 'payment_invalid_amount',COUNT(*) FROM central_db.payment_order
 WHERE total_cents<0 OR paid_cents<0 OR refunded_cents<0 OR refunded_cents>paid_cents OR paid_cents>total_cents
UNION ALL SELECT 'payment_refunded_vs_records',COUNT(*) FROM central_db.payment_order p
 LEFT JOIN (SELECT payment_order_id,SUM(refund_cents) AS total FROM central_db.refund_record WHERE status='success' GROUP BY payment_order_id) r ON r.payment_order_id=p.id
 WHERE p.refunded_cents<>COALESCE(r.total,0)
UNION ALL SELECT 'wallet_part_missing_refund_or_owner',COUNT(*) FROM central_db.wallet_refund_part p
 LEFT JOIN central_db.refund_record r ON r.id=p.refund_record_id
 LEFT JOIN central_db.wallet_account w ON w.id=p.wallet_account_id
 WHERE r.id IS NULL OR w.id IS NULL OR r.user_id<>w.user_id OR r.refund_cents<>p.amount_cents OR r.biz_type<>'wallet_recharge'
UNION ALL SELECT 'ledger_missing_wallet_or_owner',COUNT(*) FROM central_db.wallet_txn t
 LEFT JOIN central_db.wallet_account w ON w.id=t.wallet_account_id WHERE w.id IS NULL OR w.user_id<>t.user_id
UNION ALL SELECT 'active_port_missing_order_or_owner',COUNT(*) FROM central_db.active_port_charge a
 LEFT JOIN central_db.charge_order c ON c.id=a.charge_order_id
 WHERE c.id IS NULL OR c.device_id<>a.device_id OR c.port_no<>a.port_no OR c.user_id<>a.user_id
UNION ALL SELECT 'charge_order_cross_month_number',COUNT(*) FROM
 (SELECT order_no FROM central_db.charge_order GROUP BY order_no HAVING COUNT(*)>1) c
UNION ALL SELECT 'payment_order_cross_month_number',COUNT(*) FROM
 (SELECT order_no FROM central_db.payment_order GROUP BY order_no HAVING COUNT(*)>1) p
UNION ALL SELECT 'refund_cross_month_number',COUNT(*) FROM
 (SELECT refund_no FROM central_db.refund_record GROUP BY refund_no HAVING COUNT(*)>1) r
UNION ALL SELECT 'wallet_txn_cross_month_number',COUNT(*) FROM
 (SELECT txn_no FROM central_db.wallet_txn GROUP BY txn_no HAVING COUNT(*)>1) t;
COMMIT;
