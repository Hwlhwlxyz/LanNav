package listen

import (
	"net/netip"
	"testing"
)

// 样例取自 x86（小端）机器上的 /proc/net/tcp 和 tcp6。
func TestParseAddr(t *testing.T) {
	cases := map[string]Port{
		"0100007F:1F90":                         {netip.MustParseAddr("127.0.0.1"), 8080},
		"00000000:0016":                         {netip.MustParseAddr("0.0.0.0"), 22},
		"00000000000000000000000001000000:0050": {netip.MustParseAddr("::1"), 80},
	}
	for in, want := range cases {
		got, err := parseAddr(in)
		if err != nil || got != want {
			t.Errorf("parseAddr(%q) = %v, %v，应为 %v", in, got, err, want)
		}
	}
}
