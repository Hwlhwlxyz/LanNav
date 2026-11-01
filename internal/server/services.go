package server

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"lannav/internal/scan"
	"lannav/internal/store"
)

// ——— 发现的服务 ———
//
// 导航页左侧多出来的那一栏：把编辑页里设好的几个地址扫一遍，开着的网页服务
// 直接当卡片列出来。结果只存在内存里（scan.Cache）：启动时看一眼本机（Warm，
// 读系统监听表，不扫描），之后不定时刷新，用户点「刷新」才重扫。它跟普通分组渲染在同一个页面里，搜索照样能搜到。

// maxHosts 是扫描设置里最多能填几个地址。
//
// maxProbes 卡的是「地址数 × 端口数」：几个地址共用同一个并发池，总量才决定一轮要多久。
// 20000 个探测最坏 ≈ 79 轮 × 0.6s ≈ 47s（算法见 scan 包 portLimit 上面的注释），
// 加上开着的端口各自的 HTTP 探测，也在刷新的 3 分钟总时限以内。不卡这一道，
// 超时那一刻还没扫到的端口会被悄悄漏掉，结果看着正常，其实不全。
const (
	maxHosts  = 16
	maxProbes = 20000
)

// Services 是「发现的服务」那一栏要的全部数据，模板只管输出。
type Services struct {
	ID      string
	Items   []store.Item
	At      string // 上次扫描的时间，空串表示还没扫过
	Took    string
	Other   int    // 开着但不是网页的端口数（SSH、数据库这些），只报个数
	Running bool   // 正在扫，页面会自己等它扫完
	Stale   bool   // 结果是按旧设置扫的，设置后来改过
	Skipped string // 启动那一轮没扫的地址，顿号隔开；空串表示都扫了
}

// services 把缓存里的结果翻成卡片。没设扫描地址时返回 nil，导航页上就没有这一栏。
func (s *Server) services(cfg store.ScanConfig, r *http.Request) *Services {
	if len(cfg.Hosts) == 0 {
		return nil
	}
	last, running := s.scans.Last()
	v := &Services{ID: store.ServicesID, Running: running}
	if last == nil {
		return v
	}
	v.At = last.At.Format("01-02 15:04")
	v.Took = last.Took.Round(100 * time.Millisecond).String()
	if last.Took < 100*time.Millisecond {
		v.Took = "不到 0.1s" // Round 下来是 "0s"，看着像没扫
	}
	v.Stale = !slices.Equal(last.Hosts, cfg.Hosts) || last.Ports != cfg.Ports
	v.Skipped = strings.Join(last.Skipped, "、")

	self := selfHosts(r)
	for _, h := range last.Hits {
		if !h.HTTP {
			v.Other++ // 导航页放的是能点开的链接
			continue
		}
		v.Items = append(v.Items, card(h, self[h.Host]))
	}
	return v
}

// card 把一个扫描结果翻成一张卡片。名字和描述的取法跟编辑页扫描对话框的一致
// （admin.js 的 render 和 tag），两处看到的同一个服务叫同一个名字。
//
// 扫的是本机的话，http 服务的链接写成 ":8096"，交给 resolve 按这次请求的地址补全：
// 从手机按 IP 打开导航页，点出去就是那个 IP，而不是服务端眼里的 127.0.0.1。
func card(h scan.Hit, self bool) store.Item {
	port := strconv.Itoa(h.Port)
	it := store.Item{Name: h.Title, URL: h.URL, Icon: "🔌"}
	if self && strings.HasPrefix(h.URL, "http://") {
		it.URL = ":" + port
	}
	if it.Name == "" {
		it.Name = h.Hint
	}
	if it.Name == "" {
		if self {
			it.Name = ":" + port
		} else {
			it.Name = h.Host + ":" + port
		}
	}

	where := h.Host
	if self {
		where = "本机"
	}
	desc := []string{where + ":" + port}
	if h.Node != "" {
		desc = []string{h.Node + " · " + where + ":" + port + "（导航页节点）"} // 它自己报的，没扫描
	}
	if h.Server != "" {
		desc = append(desc, h.Server)
	}
	if h.Status != 0 && h.Status != http.StatusOK {
		desc = append(desc, fmt.Sprintf("HTTP %d", h.Status))
	}
	it.Description = strings.Join(desc, " · ")
	return it
}

// selfHosts 是「跑着这个导航页的那台机器」的几种叫法，跟 admin.js 的 isSelf 同一套：
// 这次请求用的地址、服务端自己算出来的局域网 IP、还有回环。
func selfHosts(r *http.Request) map[string]bool {
	m := map[string]bool{hostOf(r): true, "localhost": true, "127.0.0.1": true, "::1": true}
	if ip := scan.LocalIP(); ip != "" {
		m[ip] = true
	}
	return m
}

// refresh 处理 POST /api/services/refresh：按 data.json 里当前的设置扫一轮，
// 扫完才返回，页面拿到 204 就整页重载。设置是点的这一刻现读的，
// 编辑页改完保存，不用重启就按新设置扫。
//
// 浏览器中途断开也不要紧：扫描挂在后台，这一轮会照样扫完存进缓存。
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	_, d, _, err := s.Store.Raw()
	if err != nil {
		http.Error(w, "读取数据失败："+err.Error(), http.StatusInternalServerError)
		log.Println("services load:", err)
		return
	}
	if len(d.Scan.Hosts) == 0 {
		http.Error(w, "还没设置要扫的地址，去编辑页 /admin 的「发现服务」里填", http.StatusBadRequest)
		return
	}
	if err := checkScan(d.Scan); err != nil { // 保存时校验过，这里拦的是手改的 data.json
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case <-s.scans.Refresh(d.Scan.Hosts, d.Scan.Ports):
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

// Warm 在启动时按 data.json 里的设置，把其中本机的地址在后台过一遍存进缓存，
// 第一次打开导航页就有本机的服务，不用先点刷新。别的地址只问一下是不是导航页节点，
// 不扫描（见 scan.Cache.Warm）。
// 不阻塞启动；读不出设置或设置不对只记日志 —— 导航页打开时自会报错，不该为这个起不来。
func (s *Server) Warm() {
	_, d, _, err := s.Store.Raw()
	if err != nil {
		log.Println("services warm:", err)
		return
	}
	if len(d.Scan.Hosts) == 0 {
		return
	}
	if err := checkScan(d.Scan); err != nil {
		log.Println("services warm:", err)
		return
	}
	s.scans.Warm(d.Scan.Hosts, d.Scan.Ports)
}

// checkScan 校验扫描设置。没填地址就是没开这个功能，端口写了也只在非空时检查。
func checkScan(c store.ScanConfig) error {
	if len(c.Hosts) > maxHosts {
		return fmt.Errorf("扫描地址最多填 %d 个", maxHosts)
	}
	for _, h := range c.Hosts {
		if !scan.ValidHost(h) {
			return fmt.Errorf("扫描地址 %q 不对：%w", h, scan.ErrHost)
		}
	}
	if len(c.Hosts) == 0 && c.Ports == "" {
		return nil
	}
	ports, err := scan.ParsePorts(c.Ports)
	if err != nil {
		return fmt.Errorf("扫描端口：%w", err)
	}
	if n := len(c.Hosts) * len(ports); n > maxProbes {
		return fmt.Errorf("%d 个地址 × %d 个端口 = %d，一轮最多 %d，不然 3 分钟内扫不完。少填几个地址，或者把端口范围缩小一点",
			len(c.Hosts), len(ports), n, maxProbes)
	}
	return nil
}
