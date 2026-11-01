package peer

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sync"
	"time"

	"lannav/internal/listen"
	"lannav/internal/scan"
)

// cacheFor 是 /api/ports 结果留多久。别的节点点一次刷新、或者几个标签页一起开，
// 都只在本机探一遍；端口开关本来就不是秒级的事。
const cacheFor = 30 * time.Second

// Routes 把节点的接口挂到 mux 上。serve 为 false（-peer=false）时不回答别人的
// /api/ports，这台机器不被查询；编辑页的「查找节点」照样能用，那是去找别人。
func Routes(mux *http.ServeMux, serve bool) {
	if serve {
		mux.HandleFunc("GET /api/ports", ports)
	}
	mux.HandleFunc("POST /api/peers/find", find)   // UDP 广播找同网段的节点
	mux.HandleFunc("POST /api/peers/sweep", sweep) // 逐个询问网段里的 IP，用户点了才做
}

// ——— GET /api/ports：报本机开着的服务 ———
//
// 探一遍本机要好几秒：SMB、RPC 这类不说 HTTP 的端口，取名字时 http、https
// 各要等满一次超时。可问的一方只等 nodeTimeout（1.5s）。所以探测不挂在请求上：
// 对方挂断了，这一轮照样在后台探完 —— 挂在请求上的话，半路被取消的端口会被当成
// 关着，一份残缺的列表还会被缓存起来，接下来 30 秒谁问都拿到错的。
//
// 平时手里总有一份结果：过期了先把旧的回出去，后台再探一遍（Warm 在启动时先探好）。
// 只有从来没探过时才让请求等着。

var (
	mu      sync.Mutex
	cached  *Report
	lastErr error
	done    chan struct{} // 非 nil 表示正在探，探完会被 close
)

// probe 是真正去探本机的那一步。测试里换成假的，不去连真实端口。
var probe = probeSelf

// Warm 在后台先把本机探一遍，第一个来问的节点就不用等。
// 只碰本机自己正在监听的端口（读监听表，每个发一次 HTTP 请求取名字），
// 跟「发现的服务」启动时看本机是同一类动作，不拨关着的端口、不碰别的机器。
func Warm() {
	mu.Lock()
	start()
	mu.Unlock()
}

// start 开一轮后台探测，已经在探就沿用那一轮。调用方持有 mu。
func start() chan struct{} {
	if done != nil {
		return done
	}
	d := make(chan struct{})
	done = d
	go func() {
		r, err := probe()
		mu.Lock()
		if err == nil {
			cached = r
		}
		lastErr, done = err, nil
		mu.Unlock()
		close(d)
	}()
	return d
}

// ports 只读监听表，不扫描；对在监听的端口发一次 HTTP 请求取名字。
// 只报本机自己的端口，不会再去问别的节点 —— 节点之间互相问就成死循环了。
func ports(w http.ResponseWriter, r *http.Request) {
	rep, err := report(r.Context())
	if err != nil {
		http.Error(w, "本机的端口还没探出来："+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(rep)
}

func report(ctx context.Context) (*Report, error) {
	mu.Lock()
	r := cached
	var wait chan struct{}
	if r == nil || time.Since(r.At) >= cacheFor {
		wait = start()
	}
	mu.Unlock()
	if r != nil {
		return r, nil // 过期的也先回：端口开关不是秒级的事，新的在后台探
	}
	select {
	case <-wait:
	case <-ctx.Done():
		return nil, ctx.Err() // 对方等不及了；后台那一轮照样探完，下回就有
	}
	mu.Lock()
	defer mu.Unlock()
	if cached == nil {
		return nil, lastErr
	}
	return cached, nil
}

func probeSelf() (*Report, error) {
	ls, err := listen.TCP()
	if err != nil {
		return nil, err
	}
	name, _ := os.Hostname()
	rep := &Report{App: app, V: ver, Name: name, Ports: []NodePort{}}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for host, ps := range targets(ls) {
		for _, f := range scan.Run(ctx, host, ps) {
			rep.Ports = append(rep.Ports, toNode(f))
		}
	}
	slices.SortFunc(rep.Ports, func(a, b NodePort) int { return a.Port - b.Port })
	rep.At = time.Now() // 探完的时刻，缓存从这里算
	log.Printf("节点：探了本机的监听端口，报给别人 %d 个", len(rep.Ports))
	return rep, nil
}

// targets 把监听表分成「拿哪个地址去探 → 哪些端口」。
// 只绑在回环上的不要：别的机器连不上，报了也打不开。
// 绑全部网卡的从回环去探；绑了具体局域网地址的只能从那个地址探。
// 同一个端口绑了好几处的只探一次。
func targets(ls []listen.Port) map[string][]int {
	seen := map[int]bool{}
	out := map[string][]int{}
	add := func(host string, p int) {
		if !seen[p] {
			seen[p] = true
			out[host] = append(out[host], p)
		}
	}
	// 先收绑全部网卡的：同一个端口两种都有时，从回环探最稳
	for _, l := range ls {
		if a := l.Addr.Unmap(); a.IsUnspecified() {
			if a.Is4() {
				add("127.0.0.1", l.Port)
			} else {
				add("::1", l.Port)
			}
		}
	}
	for _, l := range ls {
		if a := l.Addr.Unmap(); !a.IsUnspecified() && !a.IsLoopback() {
			add(a.String(), l.Port)
		}
	}
	return out
}

// ——— 编辑页的「查找节点」———

// Peer 是找到的一个节点。
type Peer struct {
	Host  string `json:"host"`
	Port  int    `json:"port"` // 它的网页端口
	Name  string `json:"name"`
	Asked bool   `json:"asked"` // 端口在 NodePorts 里：加进扫描设置后能直接问它，不然只能扫
}

func newPeer(host string, port int, name string) Peer {
	return Peer{Host: host, Port: port, Name: name, Asked: slices.Contains(NodePorts, port)}
}

func find(w http.ResponseWriter, r *http.Request) {
	ps, err := Find(r.Context())
	if err != nil {
		http.Error(w, "发广播失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("查找节点：广播找到 %d 个", len(ps))
	reply(w, ps)
}

func sweep(w http.ResponseWriter, r *http.Request) {
	ps, err := Sweep(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("查找节点：逐个询问网段找到 %d 个", len(ps))
	reply(w, ps)
}

func reply(w http.ResponseWriter, ps []Peer) {
	if ps == nil {
		ps = []Peer{} // 回 [] 而不是 null，前端少一个判断
	}
	slices.SortFunc(ps, func(a, b Peer) int {
		x, _ := netip.ParseAddr(a.Host)
		y, _ := netip.ParseAddr(b.Host)
		return x.Compare(y)
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(ps)
}
