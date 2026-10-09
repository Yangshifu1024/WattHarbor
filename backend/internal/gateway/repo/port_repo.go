package repo

import (
	"context"

	"gorm.io/gorm"
)

// 端口号与端口编码的互查。设备侧上传的心跳与历史数据带的是端口号，
// 但协议报文的地址是端口编码（设备号-端口），两者的映射只在这里维护。

// PortCodeByNumber 读取未删除端口编码；缺失保留空串，返回编码及 SQL 错误，不写库。
func PortCodeByNumber(ctx context.Context, db *gorm.DB, deviceID string, portNo uint8) (string, error) {
	var code string
	err := db.WithContext(ctx).Table("device_port").
		Where("device_id=? AND port_no=? AND deleted_at IS NULL", deviceID, portNo).
		Pluck("port_code", &code).Error
	return code, err
}
