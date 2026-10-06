// Package persist 存放与业务无关的持久化原语：分页入参与响应体、自增主键回读。
//
// 它们原先在 infrastructure/database 里，但那里同时装着各 use case 的表访问，
// 于是 use case 包一引入分页就与 database 互相依赖，形成环。原语与业务无关，
// 单独成包后依赖是单向的：use case -> persist，use case 之间不互相依赖。
package persist

// Query 是各列表接口共用的分页与筛选入参。仓储方法直接接收它；
// 接口层以别名暴露，不重复定义。
type PageQuery struct {
	Page            int    // 页码，从 1 开始
	PageSize        int    // 每页条数，1–100
	Keyword, Status string // 关键词模糊查询条件；状态过滤须落在各接口给定的白名单内
	StationID       uint64 // 设备列表的站点过滤，由调用方解析并做范围校验
}

// Page 是列表接口的统一响应体，额外带上权限清单供前端决定按钮可见性。
type Page[T any] struct {
	Items       []T      `json:"items"`                 // 当当页数据
	Total       int64    `json:"total"`                 // 符合条件的总条数
	Page        int      `json:"page"`                  // 当前页码
	PageSize    int      `json:"page_size"`             // 每页条数
	Permissions []string `json:"permissions,omitempty"` // 操作人权限码；为空时整个字段省略
}
