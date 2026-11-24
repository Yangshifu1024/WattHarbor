// Package card 管理在线卡绑定和状态，处理刷卡扣款、续购及启动结果。
package card

import (
	"context"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/device"
	"wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/money"
)

// SwipePorts 提供中央运营状态及网关物理端口信息，不负责扣款或发送设备命令。
type SwipePorts interface {
	// ResolveScan 查询 code，返回设备、端口编号。
	ResolveScan(ctx context.Context, code string) (money.ScanResult, error)
}

// SwipePricing 只读取首次刷卡所需的完整方案，不提供发布或管理能力。
type SwipePricing interface {
	// ActiveDeviceRule 读取 station、device 的当前方案；失败不得创建扣款。
	ActiveDeviceRule(ctx context.Context, station uint64, device string) (charging.Rule, error)
	// CheckDeviceScheme 检查目标设备是否支持 scheme，返回不兼容或读取错误。
	CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error
}

// SwipeInput 保留设备事件标识、卡号、端口及发生时刻。
type SwipeInput struct {
	CardNo     string    `json:"card_no"`     // 设备十进制卡号，不能以别人的卡号重放操作
	EventID    string    `json:"event_id"`    // 网关冻结的事件号，同事件不重复扣款
	PortID     string    `json:"port_id"`     // 网关真实端口编码
	OccurredAt time.Time `json:"occurred_at"` // 首次事件时刻，重放不重新检查时效
}

// SwipeError 保留校验失败阶段及原始原因；HTTP 按阶段沿用错误文案和状态。
type SwipeError struct {
	Stage string // input/replay/expired/port/paused/capability/unsupported/session/pricing/compatibility/persist
	Cause error  // 错误，供哨兵和不足余额判断；无原因时为 nil
}

// Error 返回阶段或原始原因，不改变持久状态。
func (e *SwipeError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Stage
}

// Unwrap 返回原始原因，支持 errors.Is/As。
func (e *SwipeError) Unwrap() error { return e.Cause }

// SwipeService 校验刷卡事件并提交扣款事务；设备状态和首次购买方案在事务前读取。
type SwipeService struct {
	DB      *gorm.DB                      // central 连接；由进程管理，服务提交或回滚刷卡事务
	Ports   SwipePorts                    // cmd 注入的设备发现用例
	Devices device.DeviceCapabilityReader // cmd 注入的所属设备能力
	Pricing SwipePricing                  // cmd 注入的有效方案读取
	Cutoff  SwipeCutoffReader             // 停止用例提供的最新计费截止记录查询；card 不反向引用停止实现
}

// SwipeCutoffReader 只核对订单的冻结记录，调用方已按卡会话→订单顺序取得写锁。
type SwipeCutoffReader interface {
	// FrozenInTx 在调用方事务中当前读 orderID 截止，返回冻结标志。
	FrozenInTx(tx *gorm.DB, orderID uint64) (bool, error)
}

var swipePortPattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// Apply 先按事件、卡号与端口恢复首次操作，再检查事件时效与设备条件。
// 续购使用冻结套餐；卡→钱包→会话→订单依序加锁，扣款、加时与操作记录同事务。
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
