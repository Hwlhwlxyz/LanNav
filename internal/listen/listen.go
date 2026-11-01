// Package listen 列出本机正在监听的 TCP 端口。
//
// 不靠扫描：系统自己就有一张监听表，netstat、ss 读的就是它。Windows 上调
// iphlpapi.dll 的 GetExtendedTcpTable，Linux 上读 /proc/net/tcp 和 tcp6。
// 不用挨个去连关着的端口，几毫秒读完，也不会被杀毒软件当成扫描器。
//
// 对外只有 TCP 和 Port 两样，各系统的实现在 listen_<系统>.go 里。
// 以后要换成 gopsutil 之类的库，只改这个包，用的地方不用动。
package listen

import (
	"errors"
	"net/netip"
	"slices"
)

// ErrUnsupported 表示这个系统上还没有实现（比如 macOS），调用方应当退回扫描。
var ErrUnsupported = errors.New("listen: 这个系统还不支持读取监听表")

// Port 是一个在监听的端口，以及它绑在哪个地址上。
// Addr 是 0.0.0.0 或 :: 时表示所有网卡都能连。
type Port struct {
	Addr netip.Addr
	Port int
}

// Reachable 说这个监听能不能从 ip 这个地址连上：绑全部网卡的都能，
// 绑了具体地址的只有那个地址能 —— 只绑 127.0.0.1 的，从局域网 IP 就连不上。
func (p Port) Reachable(ip netip.Addr) bool {
	a := p.Addr.Unmap()
	return a.IsUnspecified() || a == ip.Unmap()
}

// TCP 返回本机所有在监听的 TCP 端口（IPv4 和 IPv6），按端口、地址排好，去过重。
func TCP() ([]Port, error) {
	ps, err := tcp()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(ps, func(a, b Port) int {
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return a.Addr.Compare(b.Addr)
	})
	return slices.Compact(ps), nil
}
