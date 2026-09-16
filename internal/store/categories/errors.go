package categories

import "errors"

// 分类域的哨兵错误，供上层映射 HTTP 状态码。
var (
	// ErrCategoryTaken 表示分类中文名已被占用（409）。
	ErrCategoryTaken = errors.New("category name already in use")
	// ErrCategorySlugTaken 表示分类英文名已被占用（409）。
	ErrCategorySlugTaken = errors.New("category slug already in use")
	// ErrCategorySlugInvalid 表示分类英文名不合规（400）。
	ErrCategorySlugInvalid = errors.New("invalid category slug")
	// ErrCategoryNameRequired 表示分类缺少中文名（400）。
	ErrCategoryNameRequired = errors.New("category name required")
	// ErrCategoryInUse 表示分类下还挂着 server，不能删除（409）。
	ErrCategoryInUse = errors.New("category still has servers")
)
