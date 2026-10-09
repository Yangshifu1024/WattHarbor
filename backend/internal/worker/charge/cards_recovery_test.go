package charge

import (
	central "chargepilot/internal/contracts/central"
	gateway "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/protocol"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"reflect"
	"testing"
)

// recoveryCards 仅模拟持久投递接口，不宣称证明真实库的冻结或设备消费。
type recoveryCards struct {
	event          gateway.CardEvent            // 事件及模拟持久冻结值
	replies        []gateway.CardReplyRequest   // 发送的回复
	advances       []gateway.CardAdvanceRequest // 事件状态更新
	freezeCalls    int                          // 冻结请求次数
	loseFirstReply bool                         // 模拟首个回复发送后响应丢失
}

// CardEvents 返回 ctx 下同一个待投递事件 fixture。
func (f *recoveryCards) CardEvents(ctx context.Context) ([]gateway.CardEvent, error) {
	return []gateway.CardEvent{f.event}, nil
}

// FreezeCardResponse 按 in 号模拟首次写入，返回已保存的 JSON 或身份错误。
func (f *recoveryCards) FreezeCardResponse(ctx context.Context, in gateway.CardFreezeRequest) (gateway.CardFreezeReply, error) {
	f.freezeCalls++
	if in.EventKey != f.event.EventKey {
		return gateway.CardFreezeReply{}, errors.New("wrong freeze event")
	}
	stored := f.event.Response == ""
	if stored {
		f.event.Response = in.ResponseJSON
	}
	return gateway.CardFreezeReply{Stored: stored, ResponseJSON: f.event.Response}, nil
}

// AdvanceCardEvent 记录 ctx 下 in 的更新；不清除已保存回复。
func (f *recoveryCards) AdvanceCardEvent(ctx context.Context, in gateway.CardAdvanceRequest) error {
	f.advances = append(f.advances, in)
	return nil
}

// ReplyCard 记录 ctx 下 in，模拟第一次发送后的未知结果，不改变回复，无真实设备 IO。
func (f *recoveryCards) ReplyCard(ctx context.Context, in gateway.CardReplyRequest) error {
	f.replies = append(f.replies, in)
	if f.loseFirstReply && len(f.replies) == 1 {
		return errors.New("reply response lost")
	}
	return nil
}

// ResolveCardPort 返回 ctx/device/port 的端口 fixture。
func (f *recoveryCards) ResolveCardPort(ctx context.Context, device string, port uint8) (gateway.CardPortReply, error) {
	return gateway.CardPortReply{Found: true, PortCode: "original-port"}, nil
}

// recoveryCentral 记录资金查询次数，模拟明确余额不足和之后改变的余额；无真实资金副作用。
type recoveryCentral struct {
	swipes, balances int   /* 操作调用计数 */
	cents            int64 /* 当前可花费分值 */
}

// SwipeCard 记录 ctx/in 事件并返回明确不足余额；无真实扣款。
func (f *recoveryCentral) SwipeCard(ctx context.Context, in central.CardSwipeRequest) (central.CardOperationReply, error) {
	f.swipes++
	return central.CardOperationReply{}, &central.CardDeclineReply{InsufficientBalance: true}
}

// CardBalance 返回 ctx/card 卡当前余额 fixture，记录次数。
func (f *recoveryCentral) CardBalance(ctx context.Context, card string) (central.CardBalanceReply, error) {
	f.balances++
	return central.CardBalanceReply{Valid: true, BalanceCents: f.cents}, nil
}

// TestFrozenCardReplyIsUsedWithoutNewDecision 核对已经冻结的设备、会话及余额回复，禁止再次决策。
func TestFrozenCardReplyIsUsedWithoutNewDecision(t *testing.T) {
	key := uuid.NewString()
	original := gateway.CardReplyRequest{DeviceID: "original-device", Session: "010203040506", Kind: protocol.CommandCardBalance, CardNumber: 17, BalanceUnits: 88}
	raw, _ := json.Marshal(original)
	edge := &recoveryCards{event: gateway.CardEvent{EventKey: key, Payload: "already frozen input must not be reinterpreted", Response: string(raw)}}
	core := &recoveryCentral{cents: 9000}
	done, err := (CardDispatcher{Gateway: edge, Central: core}).Run(context.Background())
	if err != nil || done != 1 || len(edge.replies) != 1 || !reflect.DeepEqual(edge.replies[0], original) {
		t.Fatalf("frozen reply changed: done=%d replies=%+v err=%v", done, edge.replies, err)
	}
	if core.swipes != 0 || core.balances != 0 || edge.freezeCalls != 0 || len(edge.advances) != 1 || edge.advances[0].EventKey != key || edge.advances[0].Outcome != "done" {
		t.Fatalf("frozen event was decided again: core=%+v edge=%+v", core, edge)
	}
}

// TestLostCardReplyReusesFrozenOriginalBalance 模拟发送后响应丢失及事件重排，余额变化不得改写回复。
func TestLostCardReplyReusesFrozenOriginalBalance(t *testing.T) {
	key := uuid.NewString()
	event := protocol.Event{EventID: key, Type: protocol.CardSwipe, DeviceID: "device", CardNumber: 17, Port: 1}
	raw, _ := json.Marshal(event)
	edge := &recoveryCards{event: gateway.CardEvent{EventKey: key, Payload: string(raw)}, loseFirstReply: true}
	core := &recoveryCentral{cents: 889}
	dispatcher := CardDispatcher{Gateway: edge, Central: core}
	done, err := dispatcher.Run(context.Background())
	if err == nil || done != 0 || len(edge.replies) != 1 || edge.replies[0].BalanceUnits != 88 || edge.replies[0].Invalid || len(edge.advances) != 1 || edge.advances[0].Outcome != "retry" {
		t.Fatalf("first unknown result: done=%d err=%v edge=%+v", done, err, edge)
	}
	core.cents = 9000
	done, err = dispatcher.Run(context.Background())
	if err != nil || done != 1 || len(edge.replies) != 2 || !reflect.DeepEqual(edge.replies[0], edge.replies[1]) || core.swipes != 1 || core.balances != 1 || edge.freezeCalls != 1 || len(edge.advances) != 2 || edge.advances[1].EventKey != key || edge.advances[1].Outcome != "done" {
		t.Fatalf("recovery changed original decision: done=%d err=%v core=%+v edge=%+v", done, err, core, edge)
	}
}
