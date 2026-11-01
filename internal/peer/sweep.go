package peer

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	"lannav/internal/scan"
)

// ——— 逐个询问网段里的 IP ———
//
// 广播不通（Wi-Fi 客户端隔离、路由器拦了广播）时的备用办法：把本机所在 /24 的
// 每个 IP 都问一遍 /api/ports，只问 NodePorts 里那几个端口，不碰别的。
// 虽然每台只问一两个端口，也是在批量连一片地址，可能被安全软件注意 ——
// 所以只在用户点按钮时做，启动时、点「查找节点」时都不做。

const (
	sweepConcurrency = 16 // 自己一个池，不跟扫描抢那 256 个
	sweepTimeout     = 60 * time.Second
)

// Sweep 问一遍本机所在 /24 的其余 253 个 IP。网段按默认路由那张网卡算
// （scan.LocalIP），不管 VPN、WSL 那些虚拟网卡；网段再大也只问 /24，
// 一个 /16 是 65534 个地址，那就真成扫描了。
func Sweep(ctx context.Context) ([]Peer, error) {
	me, err := netip.ParseAddr(scan.LocalIP())
	if err != nil || !me.Is4() {
		return nil, errors.New("认不出本机的局域网 IPv4 地址，没法确定要问哪个网段")
	}
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()

	hosts := neighbors(me)
	found := make([]*Peer, len(hosts)) // 按下标写回，不用加锁
	sem := make(chan struct{}, sweepConcurrency)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if r, port, ok := Ask(ctx, h); ok {
				p := newPeer(h, port, r.Name)
				found[i] = &p
			}
		}()
	}
	wg.Wait()

	var out []Peer
	for _, p := range found {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

// neighbors 是 me 所在 /24 里除了 .0、.255 和自己以外的地址。
func neighbors(me netip.Addr) []string {
	b := me.As4()
	var out []string
	for i := 1; i <= 254; i++ {
		b[3] = byte(i)
		if a := netip.AddrFrom4(b); a != me {
			out = append(out, a.String())
		}
	}
	return out
}
