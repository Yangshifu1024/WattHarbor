package http

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"math"
	"strconv"
)

// 账户类接口共用的分页与坐标解析助手。
// readPagingDefault 在未显式传入 page_size 时使用 fallback 作为每页条数，
// 供同一接口在不同查询模式下需要不同默认值的场景使用。
func readPagingDefault(c *gin.Context, fallback int) (int, int, bool) {
	return httpapi.ReadPaging(c, fallback, httpapi.DefaultPageLimit)
}

// readCoordinates 解析可选经纬度。两个参数都缺省时 present 为 false，调用方据此走
// 未定位分支；只提供其一、格式错误或越界都按请求错误处理。
func readCoordinates(c *gin.Context) (float64, float64, bool, bool) {
	rawLon, rawLat := c.Query("longitude"), c.Query("latitude")
	if rawLon == "" && rawLat == "" {
		return 0, 0, false, true
	}
	if rawLon == "" || rawLat == "" {
		httpapi.BadRequest(c, "经纬度需同时提供")
		return 0, 0, false, false
	}
	longitude, errLon := strconv.ParseFloat(rawLon, 64)
	latitude, errLat := strconv.ParseFloat(rawLat, 64)
	if errLon != nil || errLat != nil {
		httpapi.BadRequest(c, "经纬度格式无效")
		return 0, 0, false, false
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || math.IsNaN(latitude) || math.IsInf(latitude, 0) ||
		longitude < -180 || longitude > 180 || latitude < -90 || latitude > 90 {
		httpapi.BadRequest(c, "经纬度超出有效范围")
		return 0, 0, false, false
	}
	return longitude, latitude, true, true
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// resourceWriteFailure 把存储错误映射到统一的响应信封。
func resourceWriteFailure(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "记录不存在", nil)
		return
	}
	httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
}

// rechargeFieldErrors 列出充值请求中不合法的字段。
// 返回空切片表示全部合法；各条规则的取值范围与提示文案在此集中定义。
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

// walletRefundFieldErrors 列出钱包退款请求中不合法的字段。
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
