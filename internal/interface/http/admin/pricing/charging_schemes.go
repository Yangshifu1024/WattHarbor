package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	devstore "github.com/ChargePilot2026/charge-pilot/internal/device"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
)

func (a API) registerChargingSchemes(r *gin.Engine) {
	p := "/api/v1/admin/settings/charging-schemes"
	r.GET(p, a.Auth.Require("pricing.read"), a.chargingSchemes)
	r.POST(p, a.Auth.Require("pricing.rule.create"), a.saveChargingScheme)
	r.PUT(p+"/:id", a.Auth.Require("pricing.rule.update"), a.saveChargingScheme)
	r.POST(p+"/:id/status", a.Auth.Require("pricing.rule.update"), a.chargingSchemeStatus)
	r.DELETE(p+"/:id", a.Auth.Require("pricing.rule.update"), a.deleteChargingScheme)
	r.POST(p+"/apply", a.Auth.Require("pricing.rule.create"), a.applyChargingScheme)
	r.POST(p+"/preview", a.Auth.Require("pricing.read"), a.previewChargingScheme)
	r.GET("/api/v1/admin/stations/:id/charging-scheme", a.Auth.Require("pricing.read"), a.effectiveChargingScheme)
	r.POST("/api/v1/admin/stations/:id/charging-scheme/inherit", a.Auth.Require("pricing.rule.update"), a.inheritChargingScheme)
	r.GET("/api/v1/admin/settings/device-capabilities", a.Auth.Require("pricing.read"), a.executionCapabilities)
}

// asPricingConflict 把持久化层的版本冲突哨兵转成后台统一的业务冲突，
// 让「别人刚改过这一版」返回 409 而不是 500。
func asPricingConflict(err error) error {
	if errors.Is(err, pricing.ErrPricingVersionConflict) {
		return admin.ErrConflict
	}
	return err
}

type schemeInput struct {
	Scheme          charging.Scheme `json:"scheme"`
	ExpectedVersion uint32          `json:"expected_version"`
}

func (a API) chargingSchemes(c *gin.Context) {
	rows, err := (pricing.PricingStore{DB: a.Store.AdminDB}).ListPricingTemplates(c.Request.Context())
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	items := []gin.H{}
	for _, r := range rows {
		var spec charging.Spec
		if json.Unmarshal(r.SpecJSON, &spec) != nil || spec.Scheme == nil {
			continue
		}
		items = append(items, gin.H{"id": r.ID, "scheme": spec.Scheme, "version": r.Version, "status": r.Status})
	}
	httpapi.OK(c, gin.H{"items": items, "permissions": c.MustGet("admin_profile").(operations.Profile).Permissions})
}
func (a API) saveChargingScheme(c *gin.Context) {
	var in schemeInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	in.Scheme = in.Scheme.Normalized()
	in.Scheme.Name = strings.TrimSpace(in.Scheme.Name)
	if err := in.Scheme.Validate(); err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	id := uint64(0)
	if c.Request.Method == "PUT" {
		var ok bool
		id, ok = chargehttp.AdminPathID(c)
		if !ok {
			return
		}
	}
	raw, _ := json.Marshal(in.Scheme.SpecFor(in.Scheme.Packages[0]))
	display, _ := json.Marshal(in.Scheme.Display)
	var version uint32
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		saved, savedVersion, before, err := (pricing.PricingStore{DB: tx}).SavePricingTemplate(tx, id, in.ExpectedVersion, pricing.PricingTemplateInput{
			Name: in.Scheme.Name, Remark: in.Scheme.Remark, SpecJSON: string(raw), DisplayJSON: string(display),
		})
		if err != nil {
			return err
		}
		id, version = saved, savedVersion
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "charging_scheme.save", "pricing_template", id, before, in.Scheme, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, asPricingConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": version})
}
func (a API) chargingSchemeStatus(c *gin.Context) { a.changeChargingScheme(c, false) }
func (a API) deleteChargingScheme(c *gin.Context) { a.changeChargingScheme(c, true) }
func (a API) changeChargingScheme(c *gin.Context, remove bool) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion uint32 `json:"expected_version"`
		Status          string `json:"status"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !remove && in.Status != "active" && in.Status != "disabled" {
		httpapi.BadRequest(c, "状态无效")
		return
	}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		before, err := (pricing.PricingStore{DB: tx}).ChangePricingTemplateStatus(tx, id, in.ExpectedVersion, in.Status, remove)
		if err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "charging_scheme.status", "pricing_template", id, before, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, asPricingConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

type applySchemeInput struct {
	RequestID       string           `json:"request_id"`
	StationID       uint64           `json:"station_id"`
	DeviceID        string           `json:"device_id"`
	TemplateID      uint64           `json:"template_id"`
	TemplateVersion uint32           `json:"template_version"`
	ExpectedVersion uint32           `json:"expected_version"`
	Scheme          *charging.Scheme `json:"scheme,omitempty"`
}

func (a API) applyChargingScheme(c *gin.Context) {
	var in applySchemeInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if uuid.Validate(in.RequestID) != nil || in.StationID == 0 || in.DeviceID != "" && !devstore.ValidDeviceID(in.DeviceID) || (in.TemplateID == 0) == (in.Scheme == nil) {
		httpapi.BadRequest(c, "请选择模板或完整方案，并提供请求编号与站点")
		return
	}
	if !a.RequirePricingTargetScope(c, in.StationID, in.DeviceID) {
		return
	}
	actor := c.MustGet("admin_profile").(operations.Profile)
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	var id uint64
	var version uint32
	replayed := false
	store := pricing.PricingStore{DB: a.Store.AdminDB}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 先锁目标站点再查下发凭据：并发到达的相同请求会串行化，
		// 后到的那个才能看到先到者写下的凭据，两者都返回首次发布的结果。
		if err := store.LockStation(tx, in.StationID); err != nil {
			return err
		}
		receipt, found := store.FindPublication(tx, in.RequestID)
		if found == nil {
			if receipt.ActorID != actor.ID || receipt.PayloadHash != hash {
				return admin.ErrConflict
			}
			id, version, replayed = receipt.RuleID, receipt.Version, true
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		scheme := in.Scheme
		if in.TemplateID > 0 {
			tmpl, err := store.ActivePricingTemplate(tx, in.TemplateID)
			if err != nil {
				return err
			}
			if tmpl.Version != in.TemplateVersion {
				return admin.ErrConflict
			}
			parsed, err := pricing.PricingTemplateScheme(tmpl)
			if err != nil {
				return err
			}
			scheme = &parsed
		}
		normalized := scheme.Normalized()
		scheme = &normalized
		if err := scheme.Validate(); err != nil {
			return fmt.Errorf("%w: %s", admin.ErrConflict, err)
		}
		if err := pricing.CheckSchemeTargets(tx, in.StationID, in.DeviceID, *scheme); err != nil {
			return fmt.Errorf("%w: %s", admin.ErrConflict, err)
		}
		current, err := store.CurrentRuleVersion(tx, in.StationID, in.DeviceID)
		if err != nil {
			return err
		}
		if current != in.ExpectedVersion {
			return admin.ErrConflict
		}
		version = current + 1
		snapshot, _ := json.Marshal(scheme.SpecFor(scheme.Packages[0]))
		published, err := store.PublishPricingRule(tx, in.StationID, in.DeviceID, version, pricing.PublishRuleInput{
			Name: scheme.Name, TemplateID: in.TemplateID, SpecJSON: string(snapshot),
		})
		if err != nil {
			return err
		}
		id = published
		if err := store.RecordPublication(tx, pricing.PricingPublication{
			RequestID: in.RequestID, ActorID: actor.ID, PayloadHash: hash, RuleID: id, Version: version,
		}); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, actor, "charging_scheme.apply", "pricing_rule", id, nil,
			gin.H{
				"name": scheme.Name, "station_id": in.StationID, "device_id": in.DeviceID,
				"template_id": in.TemplateID, "spec_json": string(snapshot),
				"channel": "default", "version": version, "status": "active",
			}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, asPricingConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": version, "replayed": replayed})
}

func (a API) effectiveChargingScheme(c *gin.Context) {
	station, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	device := c.Query("device_id")
	if !a.RequirePricingTargetScope(c, station, device) {
		return
	}
	db := a.Store.AdminDB.WithContext(c.Request.Context())
	store := pricing.PricingStore{DB: db}
	if device != "" {
		count, err := store.CountStationDevice(c.Request.Context(), station, device)
		if err != nil {
			chargehttp.AdminResourceFailure(c, err)
			return
		}
		if count != 1 {
			chargehttp.AdminResourceFailure(c, gorm.ErrRecordNotFound)
			return
		}
	}
	current, err := store.CurrentRuleVersion(db, station, device)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	rule, err := (pricing.PricingStore{DB: db}).ActiveDeviceRule(c.Request.Context(), station, device)
	if errors.Is(err, charging.ErrRuleUnavailable) {
		httpapi.OK(c, gin.H{"scheme": nil, "version": current, "inherited": false})
		return
	}
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"scheme": rule.Spec.Scheme, "version": current, "rule_id": rule.ID, "inherited": device != "" && rule.DeviceID == "", "permissions": c.MustGet("admin_profile").(operations.Profile).Permissions})
}

func (a API) inheritChargingScheme(c *gin.Context) {
	station, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		DeviceID        string `json:"device_id"`
		ExpectedVersion uint32 `json:"expected_version"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.DeviceID == "" || !devstore.ValidDeviceID(in.DeviceID) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	if !a.RequirePricingTargetScope(c, station, in.DeviceID) {
		return
	}
	store := pricing.PricingStore{DB: a.Store.AdminDB}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := store.LockStation(tx, station); err != nil {
			return err
		}
		rule, err := (pricing.PricingStore{DB: tx}).ActiveStationRule(c.Request.Context(), station)
		if err != nil {
			return err
		}
		if err := pricing.CheckSchemeTargets(tx, station, in.DeviceID, *rule.Spec.Scheme); err != nil {
			return fmt.Errorf("%w: %s", admin.ErrConflict, err)
		}
		current, err := store.CurrentRuleVersion(tx, station, in.DeviceID)
		if err != nil {
			return err
		}
		if current != in.ExpectedVersion {
			return admin.ErrConflict
		}
		if err := store.InheritStationRule(tx, station, in.DeviceID); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, c.MustGet("admin_profile").(operations.Profile), "charging_scheme.inherit", "station", station, nil, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"inherited": true})
}

func (a API) previewChargingScheme(c *gin.Context) {
	var in struct {
		Scheme      charging.Scheme      `json:"scheme"`
		PackageID   uint64               `json:"package_id"`
		Meter       charging.ActualMeter `json:"meter"`
		Scenario    string               `json:"scenario"`
		WalletCents int64                `json:"wallet_cents"`
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	result, err := in.Scheme.PreviewScenario(in.PackageID, in.Meter, in.Scenario, in.WalletCents)
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	httpapi.OK(c, result)
}
func (a API) executionCapabilities(c *gin.Context) {
	station, ok := admin.QueryStationID(c, true)
	if !ok {
		return
	}
	device := c.Query("device_id")
	if !devstore.ValidDeviceID(device) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	if !a.RequirePricingTargetScope(c, station, device) {
		return
	}
	adapter, cap, err := (pricing.PricingStore{DB: a.Store.AdminDB}).DeviceCapabilities(c.Request.Context(), station, device)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"protocol_adapter": adapter, "capabilities": cap, "reports_energy": cap.ReportsEnergy, "reports_segmented_power": cap.ReportsSegmentedPower})
}
