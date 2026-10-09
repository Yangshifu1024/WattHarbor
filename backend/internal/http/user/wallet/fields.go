package wallet

import (
	"chargepilot/internal/platform/httpapi"
	"github.com/google/uuid"
)

// rechargeFieldErrors 校验 requestID 的 UUID 格式与 amountCents 的 100..5000000 分范围，返回全部字段错误；空切片表示合法，无 IO。
func rechargeFieldErrors(requestID string, amountCents int64) []httpapi.FieldErr {
	var errs []httpapi.FieldErr
	if uuid.Validate(requestID) != nil {
		errs = append(errs, httpapi.FieldErr{Field: "request_id", Rule: "uuid"})
	}
	if amountCents < 100 || amountCents > 5000000 {
		errs = append(errs, httpapi.FieldErr{Field: "amount_cents", Rule: "100..5000000"})
	}
	return errs
}

// walletRefundFieldErrors 校验 requestID、amountCents 的 1..5000000 分范围及 reason 的 255 字符上限，返回全部字段错误；无 IO。
func walletRefundFieldErrors(requestID string, amountCents int64, reason string) []httpapi.FieldErr {
	var errs []httpapi.FieldErr
	if uuid.Validate(requestID) != nil {
		errs = append(errs, httpapi.FieldErr{Field: "request_id", Rule: "uuid"})
	}
	if amountCents <= 0 || amountCents > 5000000 {
		errs = append(errs, httpapi.FieldErr{Field: "amount_cents", Rule: "1..5000000"})
	}
	if len([]rune(reason)) > 255 {
		errs = append(errs, httpapi.FieldErr{Field: "reason", Rule: "max_255_runes"})
	}
	return errs
}
