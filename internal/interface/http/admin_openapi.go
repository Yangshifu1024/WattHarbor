package http

import _ "embed"

// OpenAPIDocument 嵌入运营后台的 OpenAPI 描述，供文档页及 /api/docs/admin.openapi.json 返回。
//
//go:embed admin.openapi.json
var OpenAPIDocument []byte
