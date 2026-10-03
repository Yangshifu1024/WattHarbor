// Package admin 汇总运营后台的 HTTP 接口层。
//
// 拆包的依据是业务族而非进程：admin 进程与 central 进程各自注册自己需要的族，
// 同一族的处理器不感知自己被哪个进程挂载。族之间的共享装配集中在 Deps，
// 族内的处理器以 API 内嵌 Deps 的方式持有数据句柄与鉴权中间件。
package admin

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"gorm.io/gorm"
)

// Store 是 central_db 的三个用途视图。同一套底层连接按业务归属暴露为
// 不同的句柄名，使处理器只能看到自己该写的那部分。
type Store struct{ AdminDB, UserDB, BillingDB *gorm.DB }

// Deps 是所有后台业务族的共享装配：数据句柄、鉴权中间件，
// 以及调用网关所需的地址与内部服务令牌。
type Deps struct {
	Store                    Store                // 各模块的数据库句柄
	Auth                     chargehttp.AdminAuth // 鉴权与权限中间件
	GatewayURL, ServiceToken string               // 调用网关用的地址与内部服务令牌
	ExportDir                string               // 导出任务的落地目录
}

// ErrConflict 是后台各业务族共用的乐观版本冲突哨兵。原先散在单个包里，
// 拆包后由本包统一提供，各族直接引用。
var ErrConflict = chargehttp.ErrConflict

// PageQuery 是全后台统一的分页与筛选入参，各列表接口在此基础上再拼自己的条件。
type PageQuery = chargehttp.AdminPageQuery

// Page 是后台列表接口的统一响应体。
type Page[T any] = chargehttp.AdminPage[T]
