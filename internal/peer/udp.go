package peer

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"strconv"
	"time"
)

// ——— UDP 广播：一问一答 ———
//
// 发问方往每张网卡的本网段广播地址发一句 who，每个节点收到就单独回一句
// 「我叫什么、网页开在哪个端口」。对方的 IP 不写在回话里，以收到的包的来源地址为准：
// 节点可能有好几张网卡，它自己不知道哪个是你连得上的。
//
// 只在同一网段有效（路由器不转发广播），只支持 IPv4（IPv6 没有广播）。
// 跨网段的在扫描设置里手填地址。

// UDPPort 是收广播的端口，所有机器必须一致。跟网页端口（-port）无关：
// 网页端口改成 9000 的节点照样收得到，回话里会说自己在 9000。
// 跟 NodePorts 也分开 —— 改「问哪些端口」的策略，不该连带把广播端口挪走。
// 发问方用系统随机给的端口发，节点照着来源回，所以只有收的这一端要固定。
const UDPPort = 7865

// findWait 是发完广播等回话的时间。内网一个来回几毫秒，1 秒是留给慢设备的。
const findWait = time.Second

// hello 是广播里来回的那句话。问：{"app":"lannav","v":1,"q":"who"}；
// 答：{"app":"lannav","v":1,"name":"NAS","port":7865}。
type hello struct {
	App  string `json:"app"`
	V    int    `json:"v"`
	Q    string `json:"q,omitempty"`
	Name string `json:"name,omitempty"`
	Port int    `json:"port,omitempty"`
}

// parse 认一句话：暗号不对的一律不理，同一个端口上可能还有别的程序在收广播。
func parse(b []byte) (hello, bool) {
	var h hello
	if json.Unmarshal(b, &h) != nil || h.App != app || h.V != ver {
		return hello{}, false
	}
	return h, true
}

// Listen 在后台收广播并应答，webPort 是这台机器导航页的端口。
// 端口被占了只记一条日志：这台机器不能被广播找到，导航页照常运行。
func Listen(webPort int) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: UDPPort})
	if err != nil {
		log.Printf("节点：收不了 UDP %d 上的广播，这台机器不会被「查找节点」找到：%v", UDPPort, err)
		return
	}
	name, _ := os.Hostname()
	ans, _ := json.Marshal(hello{App: app, V: ver, Name: name, Port: webPort})
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				log.Println("节点：UDP 收包出错，不再应答：", err)
				return
			}
			if h, ok := parse(buf[:n]); ok && h.Q == "who" {
				c.WriteToUDP(ans, from)
			}
		}
	}()
}

// Find 往每张网卡的本网段发一句 who，等 findWait，收齐回话。
// 自己的应答（本机也在收广播）和别的网段来的回话都丢掉。
func Find(ctx context.Context) ([]Peer, error) {
	nets := subnets()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer c.Close()

	ask, _ := json.Marshal(hello{App: app, V: ver, Q: "who"})
	// 不发 255.255.255.255：Windows 往往只从一张网卡发出去。每张网卡各发一次本网段的。
	for _, n := range nets {
		if _, err := c.WriteToUDP(ask, &net.UDPAddr{IP: bcast(n), Port: UDPPort}); err != nil {
			log.Printf("查找节点：往 %s 发广播失败：%v", n, err)
		}
	}

	deadline := time.Now().Add(findWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.SetReadDeadline(deadline)

	var out []Peer
	seen := map[string]bool{}
	buf := make([]byte, 1500)
	for {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			break // 到时间了
		}
		h, ok := parse(buf[:n])
		if !ok || h.Q != "" || h.Port < 1 || h.Port > 65535 || !accept(from.IP, nets) {
			continue
		}
		// 同一台机器多张网卡在同一网段、或者广播发了两遍，都可能回好几次
		p := newPeer(from.IP.String(), h.Port, h.Name)
		if k := net.JoinHostPort(p.Host, strconv.Itoa(p.Port)); !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// subnets 列出本机能发广播的 IPv4 网段：网卡开着、不是回环、支持广播，
// 掩码短于 /31（/31、/32 没有广播地址）。
func subnets() []*net.IPNet {
	var out []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			if ones, _ := n.Mask.Size(); ones > 0 && ones < 31 {
				out = append(out, &net.IPNet{IP: n.IP.To4(), Mask: n.Mask[len(n.Mask)-4:]})
			}
		}
	}
	return out
}

// bcast 算网段的广播地址：主机位全填 1。192.168.1.23/24 → 192.168.1.255。
func bcast(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	m := n.Mask[len(n.Mask)-4:]
	b := make(net.IP, 4)
	for i := range b {
		b[i] = ip[i] | ^m[i]
	}
	return b
}

// accept 判断一条回话收不收：得来自发过广播的某个网段，而且不是本机自己。
// 本机也在收广播，自己的应答会回来，那不是「别的节点」。
func accept(from net.IP, nets []*net.IPNet) bool {
	in := false
	for _, n := range nets {
		if n.IP.Equal(from) {
			return false // 本机某张网卡的地址
		}
		if n.Contains(from) {
			in = true
		}
	}
	return in
}
