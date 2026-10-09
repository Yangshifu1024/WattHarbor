package operations

import "errors"

// ErrConflict 表示业务记录已变化、重放内容不一致或状态不允许操作。
// 此错误由入站适配映射为冲突响应，不依赖 HTTP 框架。
var ErrConflict = errors.New("当前记录状态不允许此操作，请刷新后重试")

// ErrForbidden 表示操作人角色或当前权限不允许该业务动作。
var ErrForbidden = errors.New("当前账号无权执行此操作")
