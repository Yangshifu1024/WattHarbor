package ops

import (
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"regexp"
	"time"
	"unicode/utf8"
)

// 本族用 admin.ErrConflict 表示记录当前状态不接受这次写入
// （例如期望版本号已被别人改过），由 AdminResourceFailure 统一转成 409，
// 提示前端刷新后重试。

// registerOperations 挂载运营类接口：后台账号、告警事件、公告、
// 运营概览与公告等列表接口大多由 get 闭包统一生成，只有写操作
// 和语义特殊的白标配置需要单独的处理器。
func (a API) registerOperations(r *gin.Engine) {
	// get 生成一个"按 id 倒序列出整表"的列表接口：soft 为真时过滤软删除行，
	// columns 决定返回哪些列——敏感列在 SQL 表达式里就裁掉
	// secret 前 8 位），不会先查出来再丢弃。
	get := func(path, perm, resource string) {
		r.GET("/api/v1/admin/"+path, a.Auth.Require(perm), func(c *gin.Context) {
			spec, known := ops.AdminListSpecOf(resource)
			if !known {
				chargehttp.AdminResourceFailure(c, gorm.ErrRecordNotFound)
				return
			}
			rows, err := (ops.AdminOpsStore{DB: a.Store.AdminDB}).ListRows(c.Request.Context(), spec)
			if err != nil {
				chargehttp.AdminResourceFailure(c, err)
				return
			}
			admin.NormalizeRows(rows)
			httpapi.OK(c, gin.H{"items": rows})
		})
	}
	get("alerts", "alert.read", "alerts")
	get("announcements", "announcement.read", "announcements")
	r.POST("/api/v1/admin/admin-users", a.Auth.Require("admin_user.create"), a.createUser)
	r.POST("/api/v1/admin/alerts/:id/ack", a.Auth.Require("alert.ack"), a.ackAlert)
	r.POST("/api/v1/admin/announcements", a.Auth.Require("announcement.create"), a.createAnnouncement)
}

// normalizeRows 将数据库字节值转换为 JSON 可用的响应值。
// JSON 字段解析为数组或对象，解析失败保持原值；白名单内的 tinyint(1) 字段转为布尔值。
// 新增数据库布尔字段时需同步更新白名单，Go 计算的布尔响应字段无需登记。
// adminWrite 在同一事务中完成业务写入和审计。
// id=0 时新增并回填主键；否则锁定原记录，读取更新前快照后执行更新。
func (a API) adminWrite(c *gin.Context, table, action string, id uint64, values map[string]any) (uint64, bool) {
	p := c.MustGet("admin_profile").(operations.Profile)
	store := ops.AdminOpsStore{DB: a.Store.AdminDB}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before map[string]any
		if id != 0 {
			snapshot, err := store.LockRow(tx, table, id)
			if err != nil {
				return err
			}
			before = snapshot
			if err := store.UpdateRow(tx, table, id, values); err != nil {
				return err
			}
		} else {
			created, err := store.InsertRow(tx, table, values)
			if err != nil {
				return err
			}
			id = created
		}
		return chargehttp.AdminResourceAudit(tx, p, action, table, id, before, values, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return 0, false
	}
	return id, true
}

// createUser 校验用户名长度 3–64、密码长度 12–72 字节及角色权限。
// 角色须存在且权限非空，授予的权限不得超出调用者权限。
// 密码以 bcrypt 散列保存；审计仅记录用户名和角色。
func (a API) createUser(c *gin.Context) {
	// in 是新建后台账号的请求体，全部字段必填。
	var in struct {
		Username    string `json:"username"`     // 登录名，3–64 位英文、数字、下划线、点或连字符
		DisplayName string `json:"display_name"` // 姓名或昵称，最多 64 字符
		Password    string `json:"password"`     // 初始密码，12–72 字节，入库前做 bcrypt
		RoleID      uint64 `json:"role_id"`      // 角色 id，0 视为无效
		Phone       string `json:"phone"`        // 联系电话，可空，最长 32 字符
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`).MatchString(in.Username) || len(in.Password) < 12 || len(in.Password) > 72 || utf8.RuneCountInString(in.DisplayName) > 64 || len(in.Phone) > 32 || in.RoleID == 0 {
		httpapi.BadRequest(c, "用户名须为 3–64 位英文/数字，密码须为 12–72 字节，并选择有效角色")
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	codes, err := (ops.AdminOpsStore{DB: a.Store.AdminDB}).RolePermissionCodes(a.Store.AdminDB, in.RoleID)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	codes = operations.WithoutRetiredPermissions(codes)
	if len(codes) == 0 {
		httpapi.BadRequest(c, "角色不存在或没有任何权限")
		return
	}
	for _, code := range codes {
		if !p.HasPermission(code) {
			httpapi.Write(c, 403, 1003, "不能授予自己未拥有的权限", nil)
			return
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	var id uint64
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row := map[string]any{"username": in.Username, "display_name": in.DisplayName, "password_hash": string(hash), "phone": in.Phone, "role_id": in.RoleID, "status": "active"}
		created, err := (ops.AdminOpsStore{DB: tx}).CreateAdminUser(tx, row)
		if err != nil {
			return err
		}
		id = created
		return chargehttp.AdminResourceAudit(tx, p, "create", "admin_user", id, nil, gin.H{"username": in.Username, "role_id": in.RoleID}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// ackAlert 认领一条告警：只允许 active（未处理）→ acknowledged（已认领），
// 对已认领的告警重复提交按幂等返回成功，其它状态报冲突。认领人取当前登录者。
func (a API) ackAlert(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	store := ops.AdminOpsStore{DB: a.Store.AdminDB}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row, err := store.LockAlertStatus(tx, id)
		if err != nil {
			return err
		}
		if row.Status == "acknowledged" {
			return nil
		}
		if row.Status != "active" {
			return admin.ErrConflict
		}
		if err := store.AckAlert(tx, id, p.ID, time.Now()); err != nil {
			return err
		}
		return chargehttp.AdminResourceAudit(tx, p, "ack", "alert", id, gin.H{"status": row.Status}, gin.H{"status": "acknowledged"}, c.ClientIP(), "")
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"acknowledged": true})
}

// createAnnouncement 发布公告。scope 取 global（全局）、station（站点）、city（城市）；
// 非全局必须给 target_ids（目标站点或城市 id，最多 100 个，每项不超过 64 字符），
// end_at 为空表示长期有效。落库即 status=published，不做草稿中间态。
func (a API) createAnnouncement(c *gin.Context) {
	// in 是发布公告的请求体。
	var in struct {
		Title     string     `json:"title"`      // 公告标题，必填，最多 128 字符
		Content   string     `json:"content"`    // 公告正文，必填，最多 20000 字符
		Scope     string     `json:"scope"`      // 投放范围：global / station / city
		TargetIDs []string   `json:"target_ids"` // 目标 id 列表，scope 非 global 时必填
		StartAt   time.Time  `json:"start_at"`   // 生效时间，必填
		EndAt     *time.Time `json:"end_at"`     // 失效时间，可空表示不限；非空时必须晚于 start_at
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !operations.ValidText(in.Title, 128) || !operations.ValidText(in.Content, 20000) || in.Scope == "" || !chargehttp.AdminOneOf(in.Scope, "global station city") || in.StartAt.IsZero() || (in.EndAt != nil && !in.EndAt.After(in.StartAt)) || (in.Scope != "global" && len(in.TargetIDs) == 0) {
		httpapi.BadRequest(c, "请填写标题、内容、有效时间和公告目标")
		return
	}
	if len(in.TargetIDs) > 100 {
		httpapi.BadRequest(c, "公告目标最多 100 项")
		return
	}
	for _, t := range in.TargetIDs {
		if !operations.ValidText(t, 64) {
			httpapi.BadRequest(c, "公告目标无效")
			return
		}
	}
	targets, _ := json.Marshal(in.TargetIDs)
	id, ok := a.adminWrite(c, "announcement", "create", 0, map[string]any{"title": in.Title, "content": in.Content, "scope": in.Scope, "target_ids": string(targets), "start_at": in.StartAt, "end_at": in.EndAt, "status": "published", "created_by": c.MustGet("admin_profile").(operations.Profile).ID})
	if ok {
		httpapi.OK(c, gin.H{"id": id, "status": "published"})
	}
}
