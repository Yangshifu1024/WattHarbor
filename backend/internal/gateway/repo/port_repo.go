package repo

import (
	"context"

	"gorm.io/gorm"
)

// 设备报文使用端口序号，业务接口使用端口编码（设备号-端口号）。
// 本文件查询二者的对应关系。

// PortCodeByNumber 读取未删除端口编码；缺失保留空串，返回编码及 SQL 错误，不写库。
func PortCodeByNumber(ctx context.Context, db *gorm.DB, deviceID string, portNo uint8) (string, error) {
	var code string
	err := db.WithContext(ctx).Table("device_port").
		Where("device_id=? AND port_no=? AND deleted_at IS NULL", deviceID, portNo).
		Pluck("port_code", &code).Error
	return code, err
}
