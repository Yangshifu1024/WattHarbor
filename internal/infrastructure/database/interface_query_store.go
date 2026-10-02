package database

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 接口层零散的单条查询：用户的券、公告、支付单与卡的可用余额，
// 以及审计写入。它们各自只有一处调用点，但表名与列名是持久化知识，
// 留在处理器里就等于把库结构抄到了接口层。

// ListUserCoupons 返回用户持有的有效券，联表带出券的减免口径。
// 已软删除的券与已下架的券不出现：它们对用户没有意义，留着只会让列表
// 看起来还有可用的券。
func ListUserCoupons(ctx context.Context, db *gorm.DB, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := db.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c2 ON c2.id = g.coupon_id AND c2.deleted_at IS NULL AND c2.status = 'active'").
		Where("g.user_id = ? AND g.deleted_at IS NULL", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c2.name, " +
			"c2.discount_type, c2.discount_value_cents, c2.discount_percent, c2.min_charge_cents").
		Order("g.status ASC, g.expired_at ASC, g.id ASC").Find(&rows).Error
	return rows, err
}

// UserAnnouncementRow 是一条公告。RawIDs 存的是目标站点或城市的 JSON 原文，
// 由调用方解码后做归属判断。
type UserAnnouncementRow struct {
	ID      uint64     `gorm:"column:id"`
	Title   string     `gorm:"column:title"`
	Content string     `gorm:"column:content"`
	Scope   string     `gorm:"column:scope"`
	RawIDs  *string    `gorm:"column:target_ids"`
	StartAt time.Time  `gorm:"column:start_at"`
	EndAt   *time.Time `gorm:"column:end_at"`
}

// ListActiveAnnouncements 取当前有效的公告。
// end_at 为空表示长期有效，用 `end_at IS NULL` 显式放行，不靠零值时间比较。
//
// fetch 通常给 pageSize 的三倍：站点与城市维度的公告要按用户可达范围过滤，
// 多取一些才够填满一页。这是取数，真正的分页在过滤之后。
func ListActiveAnnouncements(ctx context.Context, db *gorm.DB, now time.Time, offset, fetch int) ([]UserAnnouncementRow, error) {
	rows := []UserAnnouncementRow{}
	err := db.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now).
		Order("start_at DESC, id DESC").Offset(offset).Limit(fetch).Find(&rows).Error
	return rows, err
}

// DevelopmentPaymentRow 是开发支付确认要核对的一笔微信支付单。
type DevelopmentPaymentRow struct {
	ID         uint64    `gorm:"column:id"`
	OrderNo    string    `gorm:"column:order_no"`
	BizType    string    `gorm:"column:biz_type"`
	TotalCents int64     `gorm:"column:total_cents"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	OpenID     string    `gorm:"column:openid"`
}

// FindDevelopmentPayment 按商户单号与用户取一笔微信支付单。
// 同时带出 openid：模拟支付要按它模拟用户身份。
func FindDevelopmentPayment(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (DevelopmentPaymentRow, error) {
	var row DevelopmentPaymentRow
	err := db.WithContext(ctx).Table("payment_order p").
		Select("p.id,p.order_no,p.biz_type,p.total_cents,p.created_at,u.openid").
		Joins("JOIN user u ON u.id=p.user_id").
		Where("p.order_no=? AND p.user_id=? AND p.pay_method='wechat'", orderNo, userID).Take(&row).Error
	return row, err
}

// CardAvailableCents 返回卡对应钱包的可用余额。
//
// 只认"用户 active + 钱包 active + 卡 active"三者同时成立：任何一环失效都
// 按余额不可用返回，而不是给出陈旧余额。查不到卡与余额为零都表达为 0，
// 调用方据此报 valid=false，不区分两者。
func CardAvailableCents(ctx context.Context, db *gorm.DB, cardNo string) (int64, error) {
	var row struct {
		BalanceCents int64 `gorm:"column:balance_cents"`
	}
	err := db.WithContext(ctx).Table("online_card c").
		Select("w.balance_cents-w.frozen_cents AS balance_cents").
		Joins("JOIN user u ON u.id=c.user_id AND u.status='active' AND u.deleted_at IS NULL").
		Joins("JOIN wallet_account w ON w.user_id=c.user_id AND w.status='active' AND w.deleted_at IS NULL").
		Where("c.card_no=? AND c.status='active'", cardNo).Take(&row).Error
	if err != nil {
		return 0, err
	}
	if row.BalanceCents < 0 {
		return 0, nil
	}
	return row.BalanceCents, nil
}

// AuditEntryInput 是一次审计写入的内容。调用方负责序列化快照，
// 但落哪张表、有哪些列、created_month 怎么算都由这里决定。
type AuditEntryInput struct {
	ActorID    uint64
	ActorName  string
	Module     string
	Action     string
	TargetType string
	TargetID   string
	BeforeJSON []byte
	AfterJSON  []byte
	ClientIP   string
	RequestID  string
}

// InsertAudit 写一条审计记录。created_month 是分区列，必须显式给：
// 这张表按月分区，省略它会写到错误的分区或直接报错。
func InsertAudit(tx *gorm.DB, in AuditEntryInput, now time.Time) error {
	return tx.Table("audit_log").Create(map[string]any{
		"actor_id": in.ActorID, "actor_name": in.ActorName, "module": in.Module,
		"action": in.Action, "target_type": in.TargetType, "target_id": in.TargetID,
		"before_json": string(in.BeforeJSON), "after_json": string(in.AfterJSON),
		"client_ip": in.ClientIP, "request_id": in.RequestID,
		"created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
	}).Error
}

// UserStationRow 是用户在地图上看到的站点。
// 经纬度在库里是 DECIMAL，这里 CAST 成字符串原样返回：调用方要拿它去算
// 距离，转成 float64 会引入精度损失，字符串让前端自己决定怎么用。
type UserStationRow struct {
	ID        uint64  `json:"id" gorm:"column:id"`
	Name      string  `json:"name" gorm:"column:name"`
	Address   *string `json:"address" gorm:"column:address"`
	Longitude string  `json:"longitude" gorm:"column:longitude"`
	Latitude  string  `json:"latitude" gorm:"column:latitude"`
	Status    string  `json:"status" gorm:"column:status"`
	Phone     *string `json:"contact_phone" gorm:"column:contact_phone"`
	// DistanceKM 只在按距离查询时填充；未定位分支下保持 nil，
	// 调用方据此表达"距离未知"，不得当成 0 公里。
	DistanceKM *float64 `json:"distance_km" gorm:"column:distance_km"`
}

// userStationSelect 刻意把经纬度 CAST 成字符串，见 UserStationRow 的说明。
const userStationSelect = "id, name, address, CAST(longitude AS CHAR) AS longitude, " +
	"CAST(latitude AS CHAR) AS latitude, status, contact_phone"

// ListActiveStations 返回启用中的站点，按创建时间倒序。
func ListActiveStations(ctx context.Context, db *gorm.DB, page, pageSize int) ([]UserStationRow, error) {
	rows := []UserStationRow{}
	err := db.WithContext(ctx).Table("station").Select(userStationSelect).
		Where("deleted_at IS NULL AND status = 'active'").
		Order("created_at DESC, id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error
	return rows, err
}

// FindStationForUser 按主键取一个未删除的站点，是否 active 由调用方判定。
func FindStationForUser(ctx context.Context, db *gorm.DB, id uint64) (UserStationRow, error) {
	var row UserStationRow
	err := db.WithContext(ctx).Table("station").Select(userStationSelect).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// ListStationScopedAnnouncements 返回站内可见的公告：全局公告加上指向本
// 站点的公告。归属判断在调用方按 target_ids 做，这里只按 scope 取数。
func ListStationScopedAnnouncements(ctx context.Context, db *gorm.DB, now time.Time, limit int) ([]UserAnnouncementRow, error) {
	rows := []UserAnnouncementRow{}
	err := db.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND scope IN ('global','station') "+
			"AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now).
		Order("start_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// StationDeviceRow 是站下一个设备的最基本状态。
type StationDeviceRow struct {
	DeviceID string `json:"device_id" gorm:"column:device_id"`
	Status   string `json:"status" gorm:"column:status"`
}

// ListStationDevices 返回站下未退役的设备，按设备号升序。
func ListStationDevices(ctx context.Context, db *gorm.DB, stationID uint64) ([]StationDeviceRow, error) {
	rows := []StationDeviceRow{}
	err := db.WithContext(ctx).Table("device_meta").Select("device_id, status").
		Where("station_id = ? AND deleted_at IS NULL AND status <> 'retired'", stationID).
		Order("device_id ASC").Find(&rows).Error
	return rows, err
}
