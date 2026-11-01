package listen

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

// 自己开一个监听，看它在不在表里。不连任何端口，只读表。
// IPv4 和 IPv6 是两张表、两种行格式，各测一遍。
func TestTCPSeesOwnListener(t *testing.T) {
	t.Run("v4", func(t *testing.T) { seesOwn(t, "tcp4", "127.0.0.1") })
	t.Run("v6", func(t *testing.T) { seesOwn(t, "tcp6", "::1") })
}

func seesOwn(t *testing.T, network, ip string) {
	l, err := net.Listen(network, net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Skip(err) // 关了 IPv6 的机器上开不出 ::1
	}
	defer l.Close()
	want := Port{netip.MustParseAddr(ip), l.Addr().(*net.TCPAddr).Port}

	ps, err := TCP()
	if errors.Is(err, ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p == want {
			return
		}
	}
	t.Fatalf("表里没有 %v（共 %d 条）", want, len(ps))
}

func TestReachable(t *testing.T) {
	lan, lo := netip.MustParseAddr("192.168.1.10"), netip.MustParseAddr("127.0.0.1")
	cases := []struct {
		bind string
		from netip.Addr
		want bool
	}{
		{"0.0.0.0", lan, true},
		{"::", lo, true},
		{"127.0.0.1", lo, true},
		{"127.0.0.1", lan, false},
		{"::ffff:192.168.1.10", lan, true},
	}
	for _, c := range cases {
		if got := (Port{netip.MustParseAddr(c.bind), 80}).Reachable(c.from); got != c.want {
			t.Errorf("绑 %s 从 %s：得到 %v，应为 %v", c.bind, c.from, got, c.want)
		}
	}
}
