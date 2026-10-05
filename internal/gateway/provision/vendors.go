package provision

import (
	"encoding/json"
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Vendor 是原公开厂商字典记录，厂商私有适配配置仍由 gateway 数据库持有。
type Vendor = gatewayrepo.Vendor

const vendorColumns = gatewayrepo.VendorColumns

// vendorInput 承载原厂商创建或编辑正文，由控制层校验并规范名称，不包含私有连接凭据。
type vendorInput struct {
	// VendorCode 原厂商业务编码。
	VendorCode string `json:"vendor_code"`
	// VendorName 原厂商名称，读取时保留可空映射。
	VendorName string `json:"vendor_name"`
	// AdapterClass 原厂商设备适配器类型。
	AdapterClass string `json:"adapter_class"`
	// Protocol 所属设备或连接的原协议名称。
	Protocol string `json:"protocol"`
	// Status 所属记录的原状态值。
	Status string `json:"status"`
}

var vendorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	errVendorCodeImmutable    = errors.New("厂商编码创建后不可修改")
	errVendorAdapterImmutable = errors.New("厂商已有设备，适配器和连接协议不可修改")
)

// registerVendors 接收非 nil r，注册原厂商列表、读取、创建与更新路由，无数据库 IO。
func (a API) registerVendors(r *gin.Engine) {
	r.GET(contract.VendorPath, a.listVendors)
	r.GET(contract.VendorPath+"/:id", a.getVendor)
	r.POST(contract.VendorPath, a.createVendor)
	r.PUT(contract.VendorPath+"/:id", a.updateVendor)
}

// vendorAuthorized 接收 c，校验原服务令牌；成功返回 true，失败写 401 并返回 false，无数据库 IO。
func (a API) vendorAuthorized(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}

// vendorDBAvailable 接收 c，数据库已配置返回 true，否则写原 503 并返回 false，不执行 SQL。
func (a API) vendorDBAvailable(c *gin.Context) bool {
	if a.DB == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "厂商管理暂时不可用", nil)
		return false
	}
	return true
}

// validateVendorInput 接收非 nil in，校验原编码、名称、适配器、协议和状态并原地去除名称两侧空白；返回公开校验错误或 nil，无 IO。
func validateVendorInput(in *vendorInput) error {
	if !vendorCodePattern.MatchString(in.VendorCode) {
		return errors.New("厂商编码须为 1–64 个字母、数字、下划线或连字符")
	}
	in.VendorName = strings.TrimSpace(in.VendorName)
	if !utf8.ValidString(in.VendorName) || utf8.RuneCountInString(in.VendorName) < 1 || utf8.RuneCountInString(in.VendorName) > 128 {
		return errors.New("厂商名称须为 1–128 个字符")
	}
	for _, ch := range in.VendorName {
		if unicode.IsControl(ch) {
			return errors.New("厂商名称不能包含控制字符")
		}
	}
	if in.AdapterClass == "" || utf8.RuneCountInString(in.AdapterClass) > 256 {
		return errors.New("请选择厂商适配器")
	}
	if in.Protocol != "tcp" && in.Protocol != "mqtt" && in.Protocol != "hybrid" {
		return errors.New("连接协议无效")
	}
	if in.Status != "enabled" && in.Status != "disabled" {
		return errors.New("厂商状态须为 enabled 或 disabled")
	}
	return nil
}

// supportedVendorAdapter 接收 in，仅 dc589 与 tcp 组合返回 true，其余 false，无 IO。
func supportedVendorAdapter(in vendorInput) bool {
	return in.AdapterClass == "dc589" && in.Protocol == "tcp"
}

// bindVendorInput 接收 c，在原正文上限内严格读取唯一 JSON 并校验、规范名称；返回 in 与成功标志，失败写原 400，不读取数据库。
func bindVendorInput(c *gin.Context) (vendorInput, bool) {
	var in vendorInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		httpapi.BadRequest(c, "厂商参数无效")
		return in, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		httpapi.BadRequest(c, "厂商参数无效")
		return in, false
	}
	if err := validateVendorInput(&in); err != nil {
		httpapi.BadRequest(c, err.Error())
		return in, false
	}
	return in, true
}

// vendorID 接收 raw，解析原正整数厂商 ID，返回身份或公开校验错误，无 IO。
func vendorID(raw string) (uint64, error) {
	if raw == "" {
		return 0, errors.New("厂商 ID 须为正整数")
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("厂商 ID 须为正整数")
		}
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("厂商 ID 须为正整数")
	}
	return id, nil
}

// vendorListQuery 保存原厂商分页、关键字、状态及指定身份筛选，由 HTTP 解析后交仓储读取。
type vendorListQuery struct {
	// Page、PageSize 原从一开始的页码。 原每页记录上限。
	Page, PageSize int
	// Keyword、Status 原厂商编码或名称筛选关键字。 所属记录的原状态值。
	Keyword, Status string
	// IDs 原指定记录身份筛选集合。
	IDs []uint64
}

// parseVendorList 接收 c，解析原分页、关键字、状态及有界去重 ID 集合；返回筛选值或错误，不写响应或执行 SQL。
func parseVendorList(c *gin.Context) (vendorListQuery, error) {
	// 分页边界与 httpapi.ReadPaging 共用常量；本接口返回 error 而非写响应，
	// 故不直接调 ReadPaging，只取同样的上下限。
	q := vendorListQuery{Page: 1, PageSize: 20, Keyword: strings.TrimSpace(c.Query("keyword")), Status: c.Query("status")}
	for name, target := range map[string]*int{"page": &q.Page, "page_size": &q.PageSize} {
		if c.Request.URL.Query().Has(name) {
			n, err := strconv.Atoi(c.Query(name))
			limit := httpapi.MaxAdminPageLimit
			if name == "page_size" {
				limit = httpapi.MaxPageSize
			}
			if err != nil || n < 1 || n > limit {
				return q, errors.New("分页参数无效：page ≥ 1，page_size 为 1–100")
			}
			*target = n
		}
	}
	if utf8.RuneCountInString(q.Keyword) > 128 || (q.Status != "" && q.Status != "enabled" && q.Status != "disabled") {
		return q, errors.New("关键词过长或状态无效")
	}
	if c.Request.URL.Query().Has("ids") {
		raw := c.Query("ids")
		parts := strings.Split(raw, ",")
		if len(parts) > 500 {
			return q, errors.New("厂商 ID 筛选最多 500 项")
		}
		seen := map[uint64]bool{}
		for _, part := range parts {
			id, err := vendorID(part)
			if err != nil {
				return q, err
			}
			if !seen[id] {
				q.IDs = append(q.IDs, id)
				seen[id] = true
			}
		}
	}
	return q, nil
}

// listVendors 接收 c，鉴权校验并转义关键字后读取厂商分页，写原列表、总数和分页值；失败写原校验或仓储错误响应，无业务写入。
func (a API) listVendors(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	q, err := parseVendorList(c)
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	pattern := ""
	if q.Keyword != "" {
		// 通配符转义：用户输入里的 % 与 _ 不该被当成模式，否则搜 "a%" 会列出全部厂商。
		pattern = "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q.Keyword) + "%"
	}
	items, total, err := gatewayrepo.ListVendors(c.Request.Context(), a.DB, gatewayrepo.VendorQuery{
		Keyword: q.Keyword, Pattern: pattern, Status: q.Status, IDs: q.IDs, Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": items, "total": total, "page": q.Page, "page_size": q.PageSize})
}

// getVendor 接收 c，鉴权并解析原厂商 id 后读取未删除厂商，写记录或原 400、401、404、503。
func (a API) getVendor(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	id, err := vendorID(c.Param("id"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	var row Vendor
	if err := a.DB.WithContext(c.Request.Context()).Select(vendorColumns).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// createVendor 接收 c，鉴权校验原支持适配器后创建厂商及启用时间；写记录或原校验、唯一键冲突、存储错误响应。
func (a API) createVendor(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	in, ok := bindVendorInput(c)
	if !ok {
		return
	}
	if !supportedVendorAdapter(in) {
		httpapi.BadRequest(c, "当前网关仅支持 dc589 适配器与 TCP 连接")
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := Vendor{VendorCode: in.VendorCode, VendorName: in.VendorName, AdapterClass: in.AdapterClass, Protocol: in.Protocol, Status: in.Status, CreatedAt: now, UpdatedAt: now}
	if in.Status == "enabled" {
		row.EnabledAt = &now
	}
	// 数据库唯一索引限制有效厂商编码，覆盖并发写入。
	if err := a.DB.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// updateVendor 接收 c，鉴权校验后在原厂商行锁下检查编码不可改与有设备时协议不可改，更新原列并回读；写记录或原冲突、校验、存储错误响应。
func (a API) updateVendor(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	id, err := vendorID(c.Param("id"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	in, ok := bindVendorInput(c)
	if !ok || !a.vendorDBAvailable(c) {
		return
	}
	var row Vendor
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		locked, err := gatewayrepo.LockVendor(tx, id)
		if err != nil {
			return err
		}
		row = locked
		if in.VendorCode != row.VendorCode {
			return errVendorCodeImmutable
		}
		if in.AdapterClass != row.AdapterClass || in.Protocol != row.Protocol {
			devices, err := gatewayrepo.CountVendorDevices(tx, row.ID)
			if err != nil {
				return err
			}
			if devices > 0 {
				return errVendorAdapterImmutable
			}
			if !supportedVendorAdapter(in) {
				return errVendorUnsupportedAdapter
			}
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		updates := map[string]any{"vendor_name": in.VendorName, "adapter_class": in.AdapterClass, "protocol": in.Protocol, "status": in.Status, "updated_at": now}
		if in.Status == "enabled" && row.Status != "enabled" {
			updates["enabled_at"] = now
		}
		updated, err := gatewayrepo.UpdateVendor(tx, row.ID, row.Status == "enabled", updates)
		if err != nil {
			return err
		}
		row = updated
		return nil
	})
	if err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

var errVendorUnsupportedAdapter = errors.New("当前网关仅支持 dc589 适配器与 TCP 连接")

// vendorFailure 接收 c 与 err，按原缺失、不可改、适配器不支持、1062 或其它错误写 404、409、400 或 503；不输出内部 SQL 错误或执行额外 SQL。
func vendorFailure(c *gin.Context, err error) {
	var mysqlErr *mysql.MySQLError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, http.StatusNotFound, 1004, "厂商不存在", nil)
	case errors.Is(err, errVendorCodeImmutable), errors.Is(err, errVendorAdapterImmutable):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	case errors.Is(err, errVendorUnsupportedAdapter):
		httpapi.BadRequest(c, err.Error())
	case errors.As(err, &mysqlErr) && mysqlErr.Number == 1062:
		httpapi.Write(c, http.StatusConflict, 2009, "厂商编码已存在", nil)
	default:
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "厂商管理暂时不可用", nil)
	}
}
