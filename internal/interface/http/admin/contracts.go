package admin

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
)

// ErrConflict 是后台各业务族共用的乐观版本冲突哨兵。原先散在单个包里，
// 拆包后由本包统一提供，各族直接引用。
var ErrConflict = chargehttp.ErrConflict

// PageQuery 是全后台统一的分页与筛选入参，各列表接口在此基础上再拼自己的条件。
type PageQuery = chargehttp.AdminPageQuery

// Page 是后台列表接口的统一响应体。
type Page[T any] = chargehttp.AdminPage[T]
