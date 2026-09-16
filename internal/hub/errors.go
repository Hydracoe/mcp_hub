package hub

import (
	"errors"

	"github.com/hydra/mcp-hub/internal/store/storeutil"
)

// ErrKeyPoolUnsupported 表示该接入方式不支持密钥池。
var ErrKeyPoolUnsupported = errors.New("该接入方式不支持密钥池")

// ErrDuplicateCredential 表示新保存的密钥与另一把密钥的请求头完全相同（O3）。
// 同一份凭据不增加任何配额，只会白占轮换名额并在界面上制造两个无法区分的条目。
var ErrDuplicateCredential = errors.New("密钥的请求头与现有密钥完全相同")

// ErrServerDisabled 表示启用工具被拒：所属 server 处于停用状态。
// 约束是「启用的工具必须挂在启用的 server 下」——停用 server 时工具已被
// 级联禁用，放行单个启用会出现工具显示已启用却对外不可见的矛盾状态。
var ErrServerDisabled = errors.New("server 已停用：请先启用 Server，再启用其中的工具")

// ErrServerNotFound 让上层区分"server 不存在"与真正的存储错误。
var ErrServerNotFound = storeutil.ErrNotFound

// IsNotFound 报告错误是否为记录不存在。
func IsNotFound(err error) bool { return errors.Is(err, storeutil.ErrNotFound) }
