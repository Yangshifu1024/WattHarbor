-- 仅用于仓库 Compose 开发库：20 个站点、每站 40 台设备、每台 6 个端口。
-- 独立厂商、站点标记和设备号段隔离原数据。连接断开时事务回滚，命名锁释放。
SET NAMES utf8mb4;
CREATE TEMPORARY TABLE fixture_guard (ok TINYINT NOT NULL CHECK (ok=1));
INSERT INTO fixture_guard VALUES (GET_LOCK('wattharbor_dev_mobile_list_fixture_v1',10));
CREATE TEMPORARY TABLE fixture_n (n INT PRIMARY KEY);
INSERT INTO fixture_n WITH RECURSIVE seq(n) AS (
  SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<40
) SELECT n FROM seq;
START TRANSACTION;
INSERT INTO gateway_db.vendor (vendor_code,vendor_name,adapter_class,protocol)
SELECT 'wh_mobile_list_v1','移动端列表验收厂商','dc589','tcp'
WHERE NOT EXISTS (SELECT 1 FROM gateway_db.vendor WHERE live_vendor_code='wh_mobile_list_v1');
SET @vendor=(SELECT id FROM gateway_db.vendor WHERE live_vendor_code='wh_mobile_list_v1' AND vendor_name='移动端列表验收厂商');
INSERT INTO fixture_guard VALUES (@vendor IS NOT NULL);

INSERT INTO central_db.station (name,address,latitude,longitude,config_json)
SELECT CONCAT('列表验收 · ',LPAD(n,2,'0'),' 号站'),CONCAT('测试园区 · 第 ',n,' 号车棚'),39.908+n/10000,116.397+n/10000,
       JSON_OBJECT('fixture','wh_mobile_list_v1','station',n)
FROM fixture_n WHERE n<=20 AND NOT EXISTS (
  SELECT 1 FROM central_db.station s WHERE s.deleted_at IS NULL
  AND s.config_json->>'$.fixture'='wh_mobile_list_v1' AND s.config_json->>'$.station'=n
);
CREATE TEMPORARY TABLE fixture_device (
  device_id VARCHAR(64) PRIMARY KEY,station_id BIGINT UNSIGNED NOT NULL,unit_no INT NOT NULL
);
INSERT INTO fixture_device
SELECT CONCAT('989000000000',LPAD(s.config_json->>'$.station',2,'0'),LPAD(n,2,'0')),s.id,n
FROM central_db.station s CROSS JOIN fixture_n WHERE s.config_json->>'$.fixture'='wh_mobile_list_v1' AND s.deleted_at IS NULL;
INSERT INTO fixture_guard SELECT COUNT(*)=800 FROM fixture_device;

INSERT INTO central_db.device_meta (device_id,station_id,vendor_id,protocol_adapter,model,status)
SELECT f.device_id,f.station_id,@vendor,'dc589','WH-LIST-V1','enabled' FROM fixture_device f
WHERE NOT EXISTS (SELECT 1 FROM central_db.device_meta d WHERE d.live_device_id=f.device_id);
INSERT INTO gateway_db.device (device_id,station_id,vendor_id,model,port_count,status)
SELECT f.device_id,f.station_id,@vendor,'WH-LIST-V1',6,'enabled' FROM fixture_device f
WHERE NOT EXISTS (SELECT 1 FROM gateway_db.device d WHERE d.device_id=f.device_id AND d.deleted_at IS NULL);
-- 如号段已被其他设备占用，整批回滚，禁止借用或覆盖既有设备。
INSERT INTO fixture_guard SELECT COUNT(*)=800 FROM fixture_device f
JOIN central_db.device_meta m ON m.live_device_id=f.device_id AND m.station_id=f.station_id AND m.vendor_id=@vendor AND m.model='WH-LIST-V1'
JOIN gateway_db.device d ON d.device_id=f.device_id AND d.station_id=f.station_id AND d.vendor_id=@vendor AND d.model='WH-LIST-V1' AND d.deleted_at IS NULL;
INSERT INTO gateway_db.device_port (device_id,port_no,port_code,status)
SELECT f.device_id,n,CONCAT(f.device_id,':',n),CASE WHEN n=5 THEN 'fault' WHEN n=4 THEN 'disabled' ELSE 'idle' END
FROM fixture_device f JOIN fixture_n ON n<=6 WHERE NOT EXISTS (
  SELECT 1 FROM gateway_db.device_port p WHERE p.device_id=f.device_id AND p.port_no=n
);
-- 模拟采样只刷新本夹具的在线快照；已有订单和端口占用均由实际业务维护。
UPDATE gateway_db.device d JOIN fixture_device f ON d.device_id=f.device_id AND d.vendor_id=@vendor AND d.model='WH-LIST-V1'
SET last_seen_at=IF(MOD(f.unit_no,5)=0,NULL,UTC_TIMESTAMP(3)),last_heartbeat_at=IF(MOD(f.unit_no,5)=0,NULL,UTC_TIMESTAMP(3));
INSERT INTO fixture_guard SELECT COUNT(*)=4800 FROM fixture_device f JOIN gateway_db.device_port p
ON p.device_id=f.device_id AND p.deleted_at IS NULL;
COMMIT;
SELECT COUNT(DISTINCT station_id) AS stations,COUNT(*) AS devices FROM fixture_device;
SELECT COUNT(*) AS ports FROM fixture_device f JOIN gateway_db.device_port p ON p.device_id=f.device_id AND p.deleted_at IS NULL;
DO RELEASE_LOCK('wattharbor_dev_mobile_list_fixture_v1');
