package provision

import (
	contract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var vendorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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
func validateVendorInput(in *contract.VendorRequest) error {
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

// bindVendorInput 接收 c，在原正文上限内严格读取唯一 JSON 并校验、规范名称；返回 in 与成功标志，失败写原 400，不读取数据库。
func bindVendorInput(c *gin.Context) (contract.VendorRequest, bool) {
	var in contract.VendorRequest
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

// listVendors 接收 c，鉴权校验后将普通关键词与原筛选交仓储，转换公开九字段并写原列表、总数和分页值。
// 空列表保持 []；失败写原校验或仓储错误响应，无业务写入。
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
	items, total, err := gatewayrepo.ListVendors(c.Request.Context(), a.DB, gatewayrepo.VendorQuery{
		Keyword: q.Keyword, Status: q.Status, IDs: q.IDs, Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		vendorFailure(c, err)
		return
	}
	reply := contract.VendorListReply{Items: make([]contract.VendorReply, 0, len(items)), Total: total, Page: q.Page, PageSize: q.PageSize}
	for _, row := range items {
		reply.Items = append(reply.Items, vendorReply(row))
	}
	httpapi.OK(c, reply)
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
	row, err := gatewayrepo.GetVendor(c.Request.Context(), a.DB, id)
	if err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, vendorReply(row))
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
	if !gatewayrepo.SupportedVendorAdapter(in.AdapterClass, in.Protocol) {
		httpapi.BadRequest(c, "当前网关仅支持 dc589 适配器与 TCP 连接")
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	row, err := gatewayrepo.CreateVendor(c.Request.Context(), a.DB, vendorRepoInput(in))
	if err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, vendorReply(row))
}

// updateVendor 接收 c，鉴权校验后将 id 与五字段交仓储执行原厂商更新事务，转换九字段记录或写原冲突、校验、存储错误响应。
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
	row, err := gatewayrepo.UpdateVendor(c.Request.Context(), a.DB, id, vendorRepoInput(in))
	if err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, vendorReply(row))
}

// vendorFailure 接收 c 与 err，按原缺失、不可改、适配器不支持、1062 或其它错误写 404、409、400 或 503；不输出内部 SQL 错误或执行额外 SQL。
func vendorFailure(c *gin.Context, err error) {
	var mysqlErr *mysql.MySQLError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, http.StatusNotFound, 1004, "厂商不存在", nil)
	case errors.Is(err, gatewayrepo.ErrVendorCodeImmutable), errors.Is(err, gatewayrepo.ErrVendorAdapterImmutable):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	case errors.Is(err, gatewayrepo.ErrVendorUnsupportedAdapter):
		httpapi.BadRequest(c, err.Error())
	case errors.As(err, &mysqlErr) && mysqlErr.Number == 1062:
		httpapi.Write(c, http.StatusConflict, 2009, "厂商编码已存在", nil)
	default:
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "厂商管理暂时不可用", nil)
	}
}

// vendorRepoInput 将 in 已校验的五字段逐项转换为仓储写入参数，返回独立值，不校验、写响应或执行 IO。
func vendorRepoInput(in contract.VendorRequest) gatewayrepo.VendorInput {
	return gatewayrepo.VendorInput{VendorCode: in.VendorCode, VendorName: in.VendorName, AdapterClass: in.AdapterClass, Protocol: in.Protocol, Status: in.Status}
}

// vendorReply 将 row 原公开九字段逐项转换为共享响应，保留启用时间 NULL 与原时间值，不查询或写入。
func vendorReply(row gatewayrepo.Vendor) contract.VendorReply {
	return contract.VendorReply{ID: row.ID, VendorCode: row.VendorCode, VendorName: row.VendorName, AdapterClass: row.AdapterClass, Protocol: row.Protocol, Status: row.Status, EnabledAt: row.EnabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
