package ops

import (
	"encoding/json"
	"strconv"
	"time"

	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
)

// AuditContext 保存变更操作者、客户端 IP 和请求号，不包含 gin 或认证令牌。
type AuditContext struct {
	Actor     operations.Profile // 已通过认证和权限检查的账号资料；数据范围另行读取
	ClientIP  string             // 可信 HTTP 适配获得的客户地址
	RequestID string             // 框架请求号，业务未指定幂等号时作为审计关联
}

// AuditChange 是与业务变更一起提交的安全快照，不应包含口令、令牌或渠道密钥。
type AuditChange struct {
	Action    string // 业务动作，如 review／release
	Target    string // 对象类型及所属模块
	ID        uint64 // 对象主键，入库转换为十进制文本
	Before    any    // 变更前快照，新建为 nil
	After     any    // 变更后业务快照
	RequestID string // 业务请求号，非空时覆盖 AuditContext.RequestID
}

// AppendAuditInTx 在业务事务内保存操作者和变更快照；失败须回滚业务变更。
// 请求号最多 64 字节，nil 快照保存为 JSON null。
func AppendAuditInTx(tx *gorm.DB, context AuditContext, change AuditChange) error {
	before, err := json.Marshal(change.Before)
	if err != nil {
		return err
	}
	after, err := json.Marshal(change.After)
	if err != nil {
		return err
	}
	requestID := change.RequestID
	if requestID == "" {
		requestID = context.RequestID
	}
	if len(requestID) > 64 {
		requestID = requestID[:64]
	}
	return InsertAudit(tx, AuditEntryInput{ActorID: context.Actor.ID, ActorName: context.Actor.Username, Module: change.Target, Action: change.Action, TargetType: change.Target, TargetID: strconv.FormatUint(change.ID, 10), BeforeJSON: before, AfterJSON: after, ClientIP: context.ClientIP, RequestID: requestID}, time.Now().UTC())
}
