package database

// 后台与运营列表接口共用的分页与筛选入参。放在持久化层是因为仓储方法
// 直接接收它；接口层以别名暴露，不重复定义。
type PageQuery struct {
	Page            int    // 页码，从 1 开始
	PageSize        int    // 每页条数，1–100
	Keyword, Status string // 关键词模糊查询条件；状态过滤，须落在各接口给定的白名单内
	StationID       uint64 // 设备列表的站点过滤，由调用方解析并做范围校验
}

// AdminPage 是后台列表接口的统一响应体，额外带上权限清单供前端决定按钮可见性。
type Page[T any] struct {
	Items       []T      `json:"items"`                 // 当页数据
	Total       int64    `json:"total"`                 // 符合条件的总条数
	Page        int      `json:"page"`                  // 当前页码
	PageSize    int      `json:"page_size"`             // 每页条数
	Permissions []string `json:"permissions,omitempty"` // 操作人权限码；为空时整个字段省略
}
