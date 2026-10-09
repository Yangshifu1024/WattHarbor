package dc589

import "encoding/binary"

const (
	// OnlineCardSwipe 是设备上报在线刷卡的命令字 0xB6。
	OnlineCardSwipe byte = 0xB6
	// OnlineCardDenied 是平台回复刷卡拒绝的命令字 0xBD。
	OnlineCardDenied byte = 0xBD
	// CardBalanceQuery 是设备查询卡余额的命令字 0xD0。
	CardBalanceQuery byte = 0xD0
	// CardBalanceReply 是平台回复卡余额的命令字 0xD1。
	CardBalanceReply byte = 0xD1
)

// CardSwipe 保存刷卡端口、卡号和设备本地扣费配置。
type CardSwipe struct {
	Port             byte
	CardNumber       uint32
	LocalDebitTenths byte // 设备本地设置的扣费额，不是平台实际收取的金额。
}

// ParseCardSwipe 解码 B6 的端口、卡号和本地扣费值。
// 四个保留字节不包含事件序号或拔卡标志；事件去重由接收方处理。
func ParseCardSwipe(f Frame) (CardSwipe, error) {
	if f.Command != OnlineCardSwipe || len(f.Data) != 10 || f.Data[0] == 0 {
		return CardSwipe{}, ErrPayload
	}
	n := binary.LittleEndian.Uint32(f.Data[1:5])
	if n == 0 {
		return CardSwipe{}, ErrPayload
	}
	return CardSwipe{Port: f.Data[0], CardNumber: n, LocalDebitTenths: f.Data[5]}, nil
}

// ParseCardBalanceQuery 解码 0xD0 的卡号；卡号零值留给调用方判断。
func ParseCardBalanceQuery(f Frame) (uint32, error) {
	if f.Command != CardBalanceQuery || len(f.Data) != 5 || f.Data[0] != 0 {
		return 0, ErrPayload
	}
	n := binary.LittleEndian.Uint32(f.Data[1:])
	return n, nil
}

// BuildCardBalanceReply 编码 0xD1；卡无效时清零余额，余额单位为 0.1 元。
func BuildCardBalanceReply(session [6]byte, card uint32, valid bool, units uint16) (Frame, error) {
	if card == 0 && valid {
		return Frame{}, ErrPayload
	}
	d := make([]byte, 7)
	if !valid {
		d[0] = 1
		units = 0
	}
	binary.LittleEndian.PutUint32(d[1:5], card)
	binary.LittleEndian.PutUint16(d[5:7], units)
	return Frame{Command: CardBalanceReply, Session: session, Data: d}, nil
}

// BuildCardDenied 编码 0xBD；无效卡清零余额，余额不足时可保留卡余额。
func BuildCardDenied(session [6]byte, card uint32, invalid bool, units uint16) (Frame, error) {
	if card == 0 {
		return Frame{}, ErrPayload
	}
	d := make([]byte, 9)
	if invalid {
		d[0] = 1
		units = 0
	}
	binary.LittleEndian.PutUint32(d[1:5], card)
	binary.LittleEndian.PutUint16(d[5:7], units)
	return Frame{Command: OnlineCardDenied, Session: session, Data: d}, nil
}

// CardBalanceUnits 将分换算为 0.1 元，向下取整并限制为 65535。
// 此值仅用于设备显示，钱包仍以分记账。
func CardBalanceUnits(cents int64) uint16 {
	if cents <= 0 {
		return 0
	}
	if cents/10 > 65535 {
		return 65535
	}
	return uint16(cents / 10)
}
