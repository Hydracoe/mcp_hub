package tests

import (
	"testing"

	"github.com/hydra/mcp-hub/internal/downstream"
	"github.com/hydra/mcp-hub/internal/store/credentials"
)

// 这组是 credentials 子包枚举归位与持久态→运行态映射用例：只走导出 API、不碰数据库，
// 故放黑盒 tests；需要未导出行结构体的用例（credentialRow → Credential）留在子包内做白盒。

// TestCredStateOrActive 钉住持久状态枚举的零值归位规则：归位只认识空值
// （库里带默认值的存量行，以及调用方用零值构造的新密钥）。不认识的取值必须
// **原样留着**——悄悄改写成 active 会让脏数据彻底隐形，这正是枚举要防的那类故障。
func TestCredStateOrActive(t *testing.T) {
	cases := []struct {
		in   credentials.CredState
		want credentials.CredState
	}{
		{"", credentials.CredStateActive},
		{credentials.CredStateActive, credentials.CredStateActive},
		{credentials.CredStateDisabled, credentials.CredStateDisabled},
		{"Active", "Active"}, // 大小写不同就是不同的值，不归位、不修正
		{"cooling", "cooling"},
	}
	for _, tc := range cases {
		if got := tc.in.OrActive(); got != tc.want {
			t.Errorf("CredState(%q).OrActive() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCredStateKeyState 钉住持久态 → 运行态的映射。持久层只有 active / disabled，
// cooling 是密钥池内存态。白名单口径：只有 active 放行，认不出的取值按不可用——
// 少一把看得见，拿脏凭据打下游不是。
func TestCredStateKeyState(t *testing.T) {
	cases := []struct {
		in   credentials.CredState
		want downstream.KeyState
	}{
		{credentials.CredStateActive, downstream.KeyActive},
		{"", downstream.KeyActive}, // 零值 = 没被判决过
		{credentials.CredStateDisabled, downstream.KeyDisabled},
		{"cooling", downstream.KeyDisabled}, // 不落库的状态 / 脏值：不发出去
		{"Active", downstream.KeyDisabled},
	}
	for _, tc := range cases {
		if got := tc.in.KeyState(); got != tc.want {
			t.Errorf("CredState(%q).KeyState() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUsable 人工开关与自动判决是两条正交的线，任一为否即不参与轮换。
func TestUsable(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		state   credentials.CredState
		want    bool
	}{
		{"启用且未被判决", true, credentials.CredStateActive, true},
		{"启用但已弃用", true, credentials.CredStateDisabled, false},
		{"人工停用", false, credentials.CredStateActive, false},
		{"既停用又弃用", false, credentials.CredStateDisabled, false},
		{"启用但状态认不出", true, "Active", false},
	}
	for _, tc := range cases {
		c := credentials.Credential{Enabled: tc.enabled, State: tc.state}
		if got := c.Usable(); got != tc.want {
			t.Errorf("%s: Usable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
