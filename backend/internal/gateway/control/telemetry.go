package control

import (
	gatewayrepo "chargepilot/internal/gateway/repo"
	"context"
	"net/http"
	"strconv"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// TelemetryAPI 提供设备读数查询；telemetry 按 ts 分区，所有查询必须包含限定起止时间的窗口。
type TelemetryAPI struct {
	// DB 装配方提供的 gateway GORM 连接句柄；此适配器不关闭连接池，具体事务由方法管理。
	DB *gorm.DB
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
	// MaxWindow 允许的最大曲线查询窗口。
	MaxWindow time.Duration
	// MaxPoints 曲线响应点数上限。
	MaxPoints int
}

// Register 在 router 注册路由；调用方须提供非 nil router。
func (a TelemetryAPI) Register(router *gin.Engine) {
	router.GET(gatewaycontract.TelemetryPath+":device_id/telemetry", a.curve)
	router.GET("/api/v1/internal/devices/:device_id/charging-samples", a.chargingSamples)
	// 批量取证挂在 /internal 根下：/devices 下已存在 :device_id 通配，
	// 静态段 charging-evidence 会与其在 gin 基数树上冲突。
	router.POST(gatewaycontract.ChargingEvidencePath, a.chargingEvidence)
	router.GET(gatewaycontract.OrderProcessPath+":order_no/process", a.chargeProcess)
	router.GET(gatewaycontract.HistoricalCurvePath+":device_id/historical-curve", a.historicalCurve)
	router.POST("/api/v1/internal/devices/:device_id/backfill", a.backfill)
	a.registerDeviceFaults(router)
	a.registerWorkerSync(router)
}

// authorized 精确验证服务请求头；成功返回 true，失败写 401 并返回 false，调用方据此退出。
func (a TelemetryAPI) authorized(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// curve 鉴权并校验设备、端口和时间窗，选择遥测粒度及点数上限后写曲线；失败写 400、401 或 503，不改变采样证据。
func (a TelemetryAPI) curve(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	if deviceID == "" || len(deviceID) > 64 {
		httpapi.BadRequest(c, "device id invalid")
		return
	}
	window := a.maxWindow()
	if window <= 0 {
		window = 24 * time.Hour
	}
	limit := a.maxPoints()
	if limit <= 0 {
		limit = 2000
	}
	to := time.Now().UTC()
	from := to.Add(-window)
	if raw := c.Query("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpapi.BadRequest(c, "from 须为 RFC 3339 时间")
			return
		}
		from = parsed.UTC()
	}
	if raw := c.Query("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpapi.BadRequest(c, "to 须为 RFC 3339 时间")
			return
		}
		to = parsed.UTC()
	}
	// 调用方不能对着分区历史要一段没有上界的切片。
	if !from.Before(to) {
		httpapi.BadRequest(c, "时间窗口无效")
		return
	}
	if to.Sub(from) > window {
		from = to.Add(-window)
	}

	ctx := c.Request.Context()
	portNo := 0
	if raw := c.Query("port_no"); raw != "" {
		var err error
		portNo, err = strconv.Atoi(raw)
		if err != nil || portNo < 1 || portNo > 255 {
			httpapi.BadRequest(c, "port_no 须为有效端口号")
			return
		}
	}
	granularity, table := chooseSource(from, to, limit)
	series, err := a.readPort(ctx, deviceID, portNo, from, to, limit, table)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "telemetry unavailable", nil)
		return
	}
	// 返回实际查询分辨率，便于调用方识别原始读数与聚合曲线。
	label := "raw"
	switch table {
	case "telemetry_aggregate_15min":
		label = "15min"
	case "telemetry_aggregate_hourly":
		label = "hourly"
	}
	httpapi.OK(c, gatewaycontract.TelemetryReply{DeviceID: deviceID, From: from, To: to, Series: series, Count: len(series), Granularity: label, Bucket: granularity.String()})
}

// read 查询设备全部端口的遥测，并按秒合并为曲线。
func (a TelemetryAPI) read(ctx context.Context, deviceID string, from, to time.Time, limit int, table string) ([]gatewaycontract.CurvePoint, error) {
	return a.readPort(ctx, deviceID, 0, from, to, limit, table)
}

// readPort 按 UTC 秒合并端口指标，只保留最新的 limit 个点并按时间升序返回；无法解析的数值跳过。
func (a TelemetryAPI) readPort(ctx context.Context, deviceID string, portNo int, from, to time.Time, limit int, table string) ([]gatewaycontract.CurvePoint, error) {
	// 长窗口使用汇总表，避免原始样本超过行数预算后仅返回窗口尾部。
	rows := []gatewayrepo.Reading{}
	var err error
	if table == "telemetry" {
		rows, err = gatewayrepo.ListRawReadings(
			ctx,
			a.DB,
			deviceID,
			portNo,
			from,
			to,
			limit,
		)
	} else {
		rows, err = gatewayrepo.ListBucketedReadings(
			ctx,
			a.DB,
			table,
			deviceID,
			portNo,
			from,
			to,
			limit,
		)
	}
	if err != nil {
		return nil, err
	}
	// bucket 保存同一 UTC 秒内按指标合并的曲线值，仅在本次查询结果转换中使用，不代表持久聚合桶。
	type bucket struct {
		// ts 时间桶时刻。
		ts time.Time
		// values 时间桶内按指标保存的数值。
		values map[string]float64
	}
	order := []time.Time{}
	buckets := map[time.Time]*bucket{}
	for _, row := range rows {
		// 同一秒内采到的样本算同一个点。
		key := row.TS.UTC().Truncate(time.Second)
		existing, present := buckets[key]
		if !present {
			existing = &bucket{ts: key, values: map[string]float64{}}
			buckets[key] = existing
			order = append(order, key)
		}
		value, err := strconv.ParseFloat(string(row.Raw), 64)
		if err != nil {
			continue
		}
		existing.values[row.Name] = value
	}
	// 先取最新的，再整体反转，曲线就能从左往右读。
	sortDesc(order)
	out := make([]gatewaycontract.CurvePoint, 0, len(order))
	for i, key := range order {
		if i >= limit {
			break
		}
		b := buckets[key]
		point := gatewaycontract.CurvePoint{TS: b.ts.Format(time.RFC3339)}
		point.PowerW = value(b.values, "power_w")
		point.CurrentA = value(b.values, "current_a")
		point.VoltageV = value(b.values, "voltage_v")
		point.TemperatureC = value(b.values, "temperature_c")
		point.BatterySOC = value(b.values, "battery_soc")
		point.MeterKWh = value(b.values, "meter_kwh")
		out = append(out, point)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// value 接收 values 与 key，存在时返回浮点数指针，缺失返回 nil；不修改映射。
func value(values map[string]float64, key string) *float64 {
	if v, present := values[key]; present {
		return &v
	}
	return nil
}

// sortDesc 接收 times，原地按时刻倒序排列。
func sortDesc(times []time.Time) {
	for i := 1; i < len(times); i++ {
		for j := i; j > 0 && times[j].After(times[j-1]); j-- {
			times[j], times[j-1] = times[j-1], times[j]
		}
	}
}

// maxWindow 返回配置 MaxWindow，由调用方处理非正默认值。
func (a TelemetryAPI) maxWindow() time.Duration { return a.MaxWindow }

// maxPoints 返回正值 MaxPoints，否则返回两千点。
func (a TelemetryAPI) maxPoints() int {
	if a.MaxPoints > 0 {
		return a.MaxPoints
	}
	return 2000
}

// chooseSource 根据窗口和点数上限选择查询表。
// 预算可覆盖整个窗口时使用原始样本；超限时多天窗口使用小时汇总，其余使用 15 分钟汇总。
func chooseSource(from, to time.Time, limit int) (time.Duration, string) {
	buckets := int(to.Sub(from) / time.Second)
	if buckets <= 0 || limit <= 0 {
		return 0, "telemetry"
	}
	// 一秒四行对一台忙碌的设备来说是相当宽松的上限估计。
	if buckets*4 <= limit*8 {
		return 0, "telemetry"
	}
	if to.Sub(from) > 6*time.Hour {
		return time.Hour, "telemetry_aggregate_hourly"
	}
	return 15 * time.Minute, "telemetry_aggregate_15min"
}
