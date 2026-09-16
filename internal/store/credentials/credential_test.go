package credentials

import (
	"testing"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// 本文件只留需要未导出符号的白盒用例（credentialRow → Credential）；
// 枚举归位与持久态→运行态映射等只走导出 API 的用例在 ../tests 黑盒目录。

// TestCredentialRowStateConversion 行 → 领域：state 为空的老行读出来是 active，
// 判决信息（kind / 时间 / 说明）与状态一起还原。
func TestCredentialRowStateConversion(t *testing.T) {
	row := credentialRow{ID: "c1", Enabled: 1}
	if got := row.toCredential().State; got != CredStateActive {
		t.Errorf("state 为空的行应读成 %q，实际 %q", CredStateActive, got)
	}

	at := time.Now()
	row.State = CredStateDisabled
	row.DisabledKind = string(downstream.VerdictInvalidKey)
	row.DisabledAt = ptrInt64(at.UnixMilli())
	row.DisabledNote = "401"
	c := row.toCredential()
	if c.State != CredStateDisabled {
		t.Errorf("state = %q, want %q", c.State, CredStateDisabled)
	}
	if c.DisabledKind != downstream.VerdictInvalidKey {
		t.Errorf("disabledKind = %q, want %q", c.DisabledKind, downstream.VerdictInvalidKey)
	}
	if !c.DisabledAt.Equal(time.UnixMilli(at.UnixMilli())) {
		t.Errorf("disabledAt = %v, want %v", c.DisabledAt, at)
	}
	if c.Usable() {
		t.Error("已弃用的密钥不该算可用")
	}
}

func ptrInt64(v int64) *int64 { return &v }
