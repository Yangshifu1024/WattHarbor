// Package dbutil 提供 UTC 月分区起点、MySQL 唯一键冲突判断和 LIKE 转义。
package dbutil

import (
	"errors"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// MonthStart 返回当前 UTC 月份的首日零点，用作 created_month。
// 该列参与主键和唯一键；写成当天日期会使同一业务号避开唯一约束，因此所有写入点必须共用本函数。
func MonthStart() time.Time {
	return MonthStartAt(time.Now())
}

// MonthStartAt 返回指定时间所在 UTC 月份的首日零点，用作历史数据的分区边界。
func MonthStartAt(at time.Time) time.Time {
	utc := at.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// IsMySQLDuplicate 判定错误是否为 MySQL 唯一键冲突（1062），
// 兼容 GORM 的 ErrDuplicatedKey 包装与 go-sql-driver 原始错误。
func IsMySQLDuplicate(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// LikePattern 转义关键词中的 !、% 和 _，避免将用户输入当作 LIKE 通配符。
func LikePattern(value string) string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(value) + "%"
}
