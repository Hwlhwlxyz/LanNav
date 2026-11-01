package scan

import (
	"context"
	"log"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"lannav/internal/listen"
)

// cacheTimeout 是导航页刷新一轮的总时限。比对话框的 scanTimeout 宽：
// 这里一次要扫好几个地址，而且没人盯着对话框等结果。
const cacheTimeout = 3 * time.Minute

// Hit 是一个地址上开着的一个端口。
type Hit struct {
	Host string
	Node string // 非空表示这个地址是导航页节点，结果是它自己报的（见 Ask）
	Found
}

// Result 是一轮扫描的结果，连同这轮用的设置 —— 设置后来改过，
// 导航页要能看出「这份结果是按旧设置扫的」。
type Result struct {
	Hosts   []string
	Ports   string
	At      time.Time
	Took    time.Duration
	Hits    []Hit    // 按设置里地址的顺序，同一地址内按端口
	Skipped []string // 启动那一轮没扫的地址（不是本机、也不是节点），点刷新才扫
}

// Cache 把导航页「发现的服务」的扫描结果存在内存里。
// 程序启动时用 Warm 看一眼本机，之后不定时刷新，用户点了刷新才重扫。
// 重启后就没了 —— 端口开没开本来就是会变的东西，不值得落盘。
//
// 本机不扫：直接读系统的监听表（listen 包），只对真在监听的那几个端口
// 发 HTTP 请求取名字。别的地址才挨个端口去拨。
//
// 零值可用。
type Cache struct {
	mu   sync.Mutex
	last *Result
	done chan struct{} // 非 nil 表示正在扫，扫完会被 close
}

// Last 返回最近一轮的结果（没扫过是 nil），以及现在是不是正在扫。
// 只在拿指针的那一下持锁，扫描期间照样能渲染页面。
func (c *Cache) Last() (r *Result, running bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.done != nil
}

// Refresh 开始一轮扫描，返回一个扫完时会被 close 的 channel。
// 已经在扫就不再开新的，直接返回那一轮的 channel —— 连点两下刷新、
// 或者两个标签页同时点，都只扫一遍。
//
// 扫描挂在后台 context 上，不跟请求走：点完刷新关掉页面，这一轮照样扫完，
// 下次打开就是新结果。ports 应当已经过 ParsePorts 校验，hosts 已经过 ValidHost。
func (c *Cache) Refresh(hosts []string, ports string) <-chan struct{} {
	return c.start(hosts, ports, false)
}

// Warm 是启动时那一轮：本机的地址读监听表，别的地址只问一句是不是节点
// （每个地址就几个 HTTP 请求，见 Ask），都不扫描。既不是本机也不是节点的跳过，
// 记在 Result.Skipped 里等用户点刷新 —— 程序一启动就自己去拨一大片端口，
// 会被杀毒软件当成扫描器。
func (c *Cache) Warm(hosts []string, ports string) {
	if Ask == nil {
		if _, err := listen.TCP(); err != nil {
			log.Println("发现服务：启动时不看本机，读不了监听表：", err)
			return
		}
		if !slices.ContainsFunc(hosts, func(h string) bool { return localIP(h).IsValid() }) {
			return // 没有本机地址，又不问节点，这一轮什么也做不了
		}
	}
	c.start(hosts, ports, true)
}

func (c *Cache) start(hosts []string, ports string, localOnly bool) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		return c.done
	}
	done := make(chan struct{})
	c.done = done

	go func() {
		r := scanAll(hosts, ports, localOnly)
		c.mu.Lock()
		c.last, c.done = r, nil
		c.mu.Unlock()
		close(done)
	}()
	return done
}

// scanAll 把几个地址一起扫。并发数由包级别的 sem 统一卡住，地址再多也不会多占网卡。
// 本机的地址只探监听表里有的端口；别的地址先问是不是节点，是就不扫了；
// localOnly 时不是节点的地址整个跳过。
func scanAll(hosts []string, ports string, localOnly bool) *Result {
	r := &Result{Hosts: hosts, Ports: ports, At: time.Now()}
	ps, err := ParsePorts(ports)
	if err != nil { // 保存时已经校验过，走到这里说明有人手改了 data.json
		log.Println("services:", err)
		return r
	}

	ctx, cancel := context.WithTimeout(context.Background(), cacheTimeout)
	defer cancel()

	// 都按下标写回，不用加锁
	per := make([][]Found, len(hosts))
	node := make([]string, len(hosts))
	skip := make([]bool, len(hosts))
	probed := make([]int, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			todo, local := Local(h, ps)
			if !local {
				if open, name, ok := askNode(ctx, h, ps); ok {
					per[i], node[i] = open, name
					return
				}
				if localOnly {
					skip[i] = true
					return
				}
			}
			probed[i] = len(todo)
			per[i] = Run(ctx, h, todo)
		}()
	}
	wg.Wait()

	probes, nodes := 0, 0
	for i, h := range hosts {
		probes += probed[i]
		if node[i] != "" {
			nodes++
		}
		if skip[i] {
			r.Skipped = append(r.Skipped, h)
		}
		for _, f := range per[i] {
			r.Hits = append(r.Hits, Hit{h, node[i], f})
		}
	}
	r.Took = time.Since(r.At)
	log.Printf("发现服务：%d 个地址里 %d 个是导航页节点（直接问的），扫了 %d 个端口，开着 %d 个，用时 %s",
		len(hosts)-len(r.Skipped), nodes, probes, len(r.Hits), r.Took.Round(100*time.Millisecond))
	return r
}

// Local 处理「扫的是本机」：是本机而且读得到监听表，就从 ports 里挑出真在监听、
// 从 host 连得上的那几个返回，local 为 true —— 拿去 Run 的只有这几个，不用挨个拨。
// 不是本机或者读不了监听表，原样返回 ports，local 为 false，照常扫。
func Local(host string, ports []int) (todo []int, local bool) {
	ip := localIP(host)
	if !ip.IsValid() {
		return ports, false
	}
	ls, err := listen.TCP()
	if err != nil {
		if err != listen.ErrUnsupported {
			log.Println("读监听表失败，本机也改用扫描：", err)
		}
		return ports, false
	}
	ok := map[int]bool{}
	for _, l := range ls {
		if l.Reachable(ip) {
			ok[l.Port] = true
		}
	}
	for _, p := range ports {
		if ok[p] {
			todo = append(todo, p)
		}
	}
	return todo, true
}

// localIP 判断 host 是不是本机：回环，或者本机某张网卡上的地址。是的话返回
// 拿去跟监听地址比的那个 IP（localhost 当 127.0.0.1），不是返回零值。
// 主机名只认 localhost，别的名字不去解析 —— 解析得慢，而且认错了也只是退回扫描。
func localIP(host string) netip.Addr {
	if host == "localhost" {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	if ip = ip.Unmap(); ip.IsLoopback() {
		return ip
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if mine, ok := netip.AddrFromSlice(n.IP); ok && mine.Unmap() == ip {
				return ip
			}
		}
	}
	return netip.Addr{}
}
