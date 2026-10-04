package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	vendorapi "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/vendor"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// asImportConflict 把持久化层的导入冲突哨兵转成后台统一的业务冲突。
func asImportConflict(err error) error {
	if errors.Is(err, device.ErrImportConflict) {
		return admin.ErrConflict
	}
	return err
}

// errAlreadyReported 表示某个 goroutine 已经写过 HTTP 响应。
// 导入任务会异步调网关，返回错误时若响应已写出，调用方据此跳过第二次写响应。
var errAlreadyReported = errors.New("response already written")

// registerImports 挂载设备批量导入的三个路由：任务列表、提交导入、重试。
// 三者共用 device.import 权限——导入本身就是开通设备的动作，查看列表也一并受它约束。
func (a API) registerImports(r *gin.Engine) {
	r.GET("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.imports)
	r.POST("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.createImport)
	r.POST("/api/v1/admin/device-imports/:import_id/retry", a.Auth.Require("device.import"), a.retryImport)
}

// imports 返回最近的导入任务（最多 100 条），供运营在看板上确认某批设备到底导进去没有。
func (a API) imports(c *gin.Context) {
	rows, err := (device.DeviceImportStore{DB: a.DB}).ListImportJobs(c.Request.Context(), 100)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, rows)
}

func requireImportScope(c *gin.Context, scope operations.DataScope, devices []device.ImportDevice) bool {
	for _, device := range devices {
		if !scope.AllowsStation(device.StationID) || !scope.AllowsVendor(device.VendorID) {
			httpapi.Write(c, 403, 1003, "导入设备的站点或厂商不在您的数据范围内", nil)
			return false
		}
	}
	return true
}

// createImport 校验 1–100 台设备的编号、归属、端口和计费模式。
// 事务内校验请求及设备身份幂等性，仅对新增设备执行能力检查；保存 pending 任务后执行导入。
func (a API) createImport(c *gin.Context) {
	var in struct {
		ImportID string                `json:"import_id"` // 导入任务号，UUID，作为幂等键
		Devices  []device.ImportDevice `json:"devices"`   // 设备清单，1–100 台，批内编号不得重复
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !admin.RequestIDPattern.MatchString(in.ImportID) || len(in.Devices) == 0 || len(in.Devices) > 100 {
		httpapi.BadRequest(c, "导入编号无效或设备数不在 1–100 范围")
		return
	}
	seen := map[string]bool{}
	for _, d := range in.Devices {
		key := strings.ToLower(d.DeviceID)
		if !device.ValidDeviceID(d.DeviceID) || d.PortCount == 0 || d.VendorID == 0 || d.StationID == 0 || seen[key] || (d.Model != nil && utf8.RuneCountInString(*d.Model) > 128) {
			httpapi.BadRequest(c, "设备编号、端口数、厂商或站点无效/重复")
			return
		}
		if d.ChargeMode != "" && !charging.ValidDeviceChargeMode(d.ChargeMode) {
			// 提交任务前校验计费模式，避免保存无法执行的导入。
			httpapi.BadRequest(c, "设备 "+d.DeviceID+" 的计费方式无效")
			return
		}
		seen[key] = true
	}
	scope, ok := admin.StationScope(c, a.DB)
	if !ok || !requireImportScope(c, scope, in.Devices) {
		return
	}
	// Resolve once per vendor; this trusted snapshot also survives import retries.
	adapters := map[uint64]string{}
	for i := range in.Devices {
		d := &in.Devices[i]
		adapter, found := adapters[d.VendorID]
		if !found {
			var vendor vendorapi.VendorRow
			if err := (vendorapi.API{DB: a.DB, Auth: a.Auth, GatewayURL: a.GatewayURL, ServiceToken: a.ServiceToken}).RequestVendor(c.Request.Context(), http.MethodGet, "/"+strconv.FormatUint(d.VendorID, 10), nil, nil, &vendor, httpapi.RequestID(c)); err != nil {
				vendorapi.VendorFailure(c, err)
				return
			}
			if vendor.Status != "enabled" || vendor.Protocol != "tcp" {
				httpapi.BadRequest(c, "请选择已启用且支持的通信协议厂商")
				return
			}
			if _, err := charging.ProtocolCapabilities(vendor.AdapterClass); err != nil {
				httpapi.BadRequest(c, err.Error())
				return
			}
			adapter = vendor.AdapterClass
			adapters[d.VendorID] = adapter
		}
		d.ProtocolAdapter = adapter
		cap, _ := charging.ProtocolCapabilities(adapter)
		d.ReportsEnergy, d.ReportsSegmentedPower = cap.ReportsEnergy, cap.ReportsSegmentedPower
	}
	body, _ := json.Marshal(in.Devices)
	p := c.MustGet("admin_profile").(operations.Profile)
	store := device.DeviceImportStore{DB: a.DB}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		prior, found := store.LockImportJob(tx, in.ImportID)
		if found == nil {
			var old []device.ImportDevice
			if json.Unmarshal([]byte(prior.RequestJSON), &old) != nil {
				return admin.ErrConflict
			}
			b, _ := json.Marshal(old)
			if string(b) != string(body) || prior.ActorID != p.ID {
				return admin.ErrConflict
			}
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		for _, d := range in.Devices {
			if _, err := store.FindActiveStation(tx, d.StationID); err != nil {
				return err
			}
			b, _ := json.Marshal(d)
			if err := store.RegisterDeviceIdentity(tx, d.DeviceID, string(b)); err != nil {
				return err
			}
			if err := store.AssertDeviceIdentity(tx, d.DeviceID, d); err != nil {
				return err
			}
		}
		// 仅新增设备需要能力校验；已归属本站点的设备保持已有配置。
		arriving, err := pricing.NewDevicesOnly(tx, in.Devices)
		if err != nil {
			return err
		}
		// 整批设备统一校验，任一设备不兼容时拒绝整批导入。
		if err := pricing.CheckImportAgainstStation(tx, arriving); err != nil {
			var blocked *pricing.ErrMeteringBlocked
			if errors.As(err, &blocked) {
				httpapi.Write(c, 409, 1009, err.Error()+"，请检查所选通信协议或站点计费方式", nil)
				return errAlreadyReported
			}
			return err
		}
		if err := store.CreateImportJob(tx, in.ImportID, p.ID, string(body)); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, p, "create", "device_import", 0, nil, in, c.ClientIP(), in.ImportID)
	})
	// 校验失败已写入响应，立即返回，避免追加第二份 JSON 响应。
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		chargehttp.AdminResourceFailure(c, asImportConflict(err))
		return
	}
	a.runImport(c, in.ImportID)
}

// retryImport 重试一笔失败的导入任务：只校验任务号格式，然后按存档的设备清单重新走一遍执行流程。
// 是否可重试由 runImport 依据任务状态判断，这里不做额外判断。
func (a API) retryImport(c *gin.Context) {
	if !admin.RequestIDPattern.MatchString(c.Param("import_id")) {
		httpapi.BadRequest(c, "导入编号无效")
		return
	}
	a.runImport(c, c.Param("import_id"))
}

// runImport 调用网关开通接口后补建尚不存在的设备元数据；请求超时为 10 秒。
// 已有设备不覆盖配置，厂商或站点不一致时拒绝。失败任务记录原因并递增 attempts；已完成任务保持幂等。
func (a API) runImport(c *gin.Context, id string) {
	scope, ok := admin.StationScope(c, a.DB)
	if !ok {
		return
	}
	var result device.ImportJob
	// 在这次有界的内部请求期间一直持着任务锁。崩溃会把元数据一起回滚，
	// 重放则能安全地看到网关那边已经持久占用的设备身份。
	store := device.DeviceImportStore{DB: a.DB}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		locked, err := store.LockImportJob(tx, id)
		if err != nil {
			return err
		}
		result = locked
		var devices []device.ImportDevice
		if err := json.Unmarshal([]byte(result.RequestJSON), &devices); err != nil {
			return err
		}
		if !requireImportScope(c, scope, devices) {
			return errAlreadyReported
		}
		if result.Status == "completed" {
			return nil
		}
		body, _ := json.Marshal(gin.H{"devices": devices})
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.GatewayURL, "/")+"/api/v1/internal/devices/provision", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Service-Token", a.ServiceToken)
			var res *http.Response
			res, err = (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err == nil {
				defer res.Body.Close()
				_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
				if res.StatusCode != 200 {
					err = fmt.Errorf("gateway HTTP %d", res.StatusCode)
				}
			}
		}
		if err != nil {
			message := "网关未确认导入，请核对厂商与设备参数后重试；dc589 最多 20 路"
			result.Status = "failed"
			result.LastError = &message
			return store.MarkImportFailed(tx, id, message)
		}
		for _, d := range devices {
			existing, e := store.LockImportedDevice(tx, d.DeviceID)
			if e == nil {
				if existing.VendorID != d.VendorID || existing.StationID != d.StationID {
					return admin.ErrConflict
				}
				continue
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			if err := store.CreateImportedDevice(tx, d); err != nil {
				return err
			}
		}
		result.Status = "completed"
		result.LastError = nil
		if err := store.MarkImportCompleted(tx, id); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "complete", "device_import", 0, nil, gin.H{"import_id": id, "count": len(devices)}, c.ClientIP(), id)
	})
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		chargehttp.AdminResourceFailure(c, asImportConflict(err))
		return
	}
	if result.Status == "failed" {
		httpapi.Write(c, 503, 5003, *result.LastError, result)
		return
	}
	httpapi.OK(c, result)
}
