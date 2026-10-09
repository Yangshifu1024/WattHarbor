package card

import (
	"context"
	"regexp"
	"strings"
	"time"

	"chargepilot/internal/device"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/money"
	"gorm.io/gorm"
)

// SwipePorts 提供中央运营状态及网关物理端口事实，不负责扣款或发送设备命令。
type SwipePorts interface {
	// ResolveScan 在 ctx 下查询原 code，返回原设备／端口身份或读取错误。
	ResolveScan(ctx context.Context, code string) (money.ScanResult, error)
}

// SwipePricing 只读取首次刷卡所需的完整方案，不提供发布或管理能力。
type SwipePricing interface {
	// ActiveDeviceRule 在 ctx 下读取 station／device 的当前方案；失败不得创建扣款。
	ActiveDeviceRule(ctx context.Context, station uint64, device string) (charging.Rule, error)
	// CheckDeviceScheme 在 ctx 下核对同一目标的 scheme，返回兼容或事实读取错误。
	CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error
}

// SwipeInput 保留设备原事件身份、卡号、端口及发生时刻。
type SwipeInput struct {
	CardNo     string    `json:"card_no"`     // 原设备十进制卡号，不能以别人的卡号重放操作
	EventID    string    `json:"event_id"`    // 网关冻结的事件号，同事件不重复扣款
	PortID     string    `json:"port_id"`     // 网关真实端口编码
	OccurredAt time.Time `json:"occurred_at"` // 首次事件时刻，重放不重新检查时效
}

// SwipeError 保留准入失败阶段及原原因；HTTP 按阶段沿用原错误文案和状态。
type SwipeError struct {
	Stage string // input/replay/expired/port/paused/capability/unsupported/session/pricing/compatibility/persist
	Cause error  // 原错误，供哨兵和不足余额判断；无原因时为 nil
}

// Error 返回阶段或原原因，不改变持久状态。
func (e *SwipeError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Stage
}

// Unwrap 返回原原因，支持 errors.Is/As；无 IO。
func (e *SwipeError) Unwrap() error { return e.Cause }

// SwipeService 拥有事件准入及原完整扣款事务；外部事实只在事务前读取。
type SwipeService struct {
	DB      *gorm.DB                      // central 连接；由进程管理，服务提交或回滚刷卡事务
	Ports   SwipePorts                    // cmd 注入的设备发现用例
	Devices device.DeviceCapabilityReader // cmd 注入的所属设备能力
	Pricing SwipePricing                  // cmd 注入的有效方案读取
	Cutoff  SwipeCutoffReader             // 所属停止用例提供的当前截止读取；card 不反向引用停止实现
}

// SwipeCutoffReader 只核对原订单的冻结事实，调用方已按卡会话→订单顺序取得写锁。
type SwipeCutoffReader interface {
	// FrozenInTx 在原 tx 中当前读 orderID 截止，返回冻结标志或存储错误；不写入或提交。
	FrozenInTx(tx *gorm.DB, orderID uint64) (bool, error)
}

var swipePortPattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// Apply 在 ctx 下处理 in，返回原操作或带阶段的准入／存储错误。
// 先按卡、端口、事件恢复原凭据，再检查首次时效及设备；续购使用原冻结套餐。
// 卡→钱包→卡会话→订单的锁序及扣款、累计时长、操作凭据保持原事务，失败全部回滚。
func (s SwipeService) Apply(ctx context.Context, in SwipeInput) (CardOperation, error) {
	fail := func(stage string, cause error) (CardOperation, error) {
		return CardOperation{}, &SwipeError{Stage: stage, Cause: cause}
	}
	if !swipePortPattern.MatchString(in.PortID) || strings.TrimSpace(in.CardNo) == "" || len(in.CardNo) > 64 || len(in.EventID) > 128 || strings.TrimSpace(in.EventID) == "" {
		return fail("input", nil)
	}
	prior, replayed, err := (UserCardRepo{DB: s.DB}).FindCardOperationByEvent(ctx, s.DB, in.EventID, in.CardNo, in.PortID)
	if err != nil {
		return fail("replay", err)
	}
	if replayed {
		return prior, nil
	}
	now := time.Now()
	if in.OccurredAt.IsZero() || now.Sub(in.OccurredAt) > 30*time.Second || in.OccurredAt.After(now.Add(time.Second)) {
		return fail("expired", nil)
	}
	resolved, err := s.Ports.ResolveScan(ctx, in.PortID)
	if err != nil || resolved.Port == nil || !resolved.Port.Online {
		return fail("port", err)
	}
	port := money.PortRef{Found: true, Kind: resolved.Kind, DeviceID: resolved.DeviceID, StationID: resolved.StationID, DeviceStatus: resolved.DeviceStatus, PortID: resolved.Port.PortID, PortNo: resolved.Port.PortNo, Online: resolved.Port.Online, Available: resolved.Port.Available}
	if port.DeviceStatus != "enabled" {
		return fail("paused", nil)
	}
	_, cap, err := s.Devices.Capabilities(ctx, port.StationID, port.DeviceID)
	if err != nil {
		return fail("capability", err)
	}
	if !cap.OnlineCard {
		return fail("unsupported", nil)
	}
	active, err := (Repo{DB: s.DB}).ActiveSessionCount(ctx, in.PortID)
	if err != nil {
		return fail("session", err)
	}
	var rule charging.Rule
	if active == 0 {
		rule, err = s.Pricing.ActiveDeviceRule(ctx, port.StationID, port.DeviceID)
		if err != nil {
			return fail("pricing", err)
		}
		if rule.Spec.Scheme != nil {
			if err = s.Pricing.CheckDeviceScheme(ctx, port.StationID, port.DeviceID, *rule.Spec.Scheme); err != nil {
				return fail("compatibility", err)
			}
		}
	}
	var out CardOperation
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = (Repo{DB: tx, Cutoff: s.Cutoff}).applyInTx(tx, in.CardNo, in.EventID, port, rule)
		return err
	})
	if err != nil {
		return fail("persist", err)
	}
	return out, nil
}
