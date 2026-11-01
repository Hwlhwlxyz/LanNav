// Package scan 做端口扫描：给一个地址，把上面开着的服务找出来，顺手认一下是什么，
// 然后在编辑页里勾几条直接变成书签。内网自建服务多半没人记得住端口，
// 这一步就是省掉「挨个试 8080、8096、9000」的功夫。
//
// 探测分两层：先 TCP 拨一下看端口开不开，开着的再当 HTTP 服务请求一次，
// 从 <title> 拿名字。拿不到就退回 Server 头，再退回常用端口的名字表。
//
// 只读不写：扫描结果不落盘。编辑页的对话框把结果回给浏览器，由用户挑完再走
// 编辑页原来那条保存路径；导航页的「发现的服务」把结果缓存在内存里（cache.go）。
package scan

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 一轮要多久，最坏情况是每个端口都等满 dialTimeout（对方防火墙丢包不回话，
// 或者 Windows 扫自己的局域网 IP —— 关着的端口要被拒绝重试两次，2s 后才报错，
// 比 dialTimeout 还长）：
//
//	最坏用时 ≈ ⌈端口数 ÷ concurrency⌉ × dialTimeout
//
// 这时候瓶颈是「干等」，不是网卡：并发翻倍，用时就减半。实测扫本机局域网 IP
// 的 2526 个端口：并发 128 用 12.0s、256 用 6.0s、512 用 3.0s，找到的端口一个不少。
// portLimit 4000 就是「单个地址最坏 10s 左右」，在对话框 scanTimeout 以内很远。
const (
	portLimit   = 4000                   // 一个地址一次最多扫多少个端口
	concurrency = 256                    // 同时在飞的探测数。没用 512：那样编出来的程序会被 Windows Defender 误报成病毒，go run 都跑不起来
	dialTimeout = 600 * time.Millisecond // 内网一个来回撑死几毫秒，600ms 足够判死
	httpTimeout = 2 * time.Second        // 开着但半天不回话的，当它不是网页
	scanTimeout = 60 * time.Second       // 整轮的总时限
)

// hint 是「开着但不是网页」的那些端口的名字。只列常见的几个：
// 它的作用是让结果里出现「22 SSH」而不是光秃秃一个端口号，
// 不是要做端口数据库 —— 那份表 /etc/services 里本来就有，这里用不上。
var hint = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS",
	139: "SMB", 445: "SMB", 548: "AFP", 631: "IPP 打印", 873: "rsync",
	1433: "SQL Server", 1521: "Oracle", 1883: "MQTT", 2049: "NFS", 2181: "ZooKeeper",
	2375: "Docker API", 2376: "Docker API (TLS)", 3306: "MySQL", 3389: "远程桌面",
	5432: "PostgreSQL", 5672: "RabbitMQ", 5900: "VNC", 6379: "Redis", 9042: "Cassandra",
	9092: "Kafka", 9100: "网络打印", 9300: "Elasticsearch 节点", 11211: "Memcached",
	27017: "MongoDB", 27018: "MongoDB 分片", 32400: "Plex", 61616: "ActiveMQ",
}

// sem 限制整个进程同时在飞的探测数。放在包级别而不是每轮各建一个：
// 编辑页的对话框和导航页的刷新可能同时在扫，它们抢的是同一张网卡。
var sem = make(chan struct{}, concurrency)

// Found 是一个开着的端口。HTTP 为 false 说明它不是网页服务。
type Found struct {
	Port   int    `json:"port"`
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	Server string `json:"server,omitempty"`
	Status int    `json:"status,omitempty"` // 401/403 也算找到了，只是要登录
	Hint   string `json:"hint,omitempty"`
	HTTP   bool   `json:"http"`
}

// Handle 处理 POST /api/scan。扫描本身不改任何东西，结果只回给浏览器。
func Handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host  string `json:"host"`
		Ports string `json:"ports"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "请求格式错误："+err.Error(), http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(req.Host)
	if !ValidHost(host) {
		http.Error(w, ErrHost.Error(), http.StatusBadRequest)
		return
	}
	ports, err := ParsePorts(req.Ports)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 总时限之外还挂在 r.Context() 上：浏览器关掉对话框、断开连接时一起取消，
	// 半路的探测立刻收手，不会留一堆连接在后台慢慢超时。
	ctx, cancel := context.WithTimeout(r.Context(), scanTimeout)
	defer cancel()
	todo, local := Local(host, ports) // 本机就只探监听表里有的，见 cache.go
	var open []Found
	node, asked := "", false
	if !local {
		open, node, asked = askNode(ctx, host, ports)
	}
	switch {
	case asked:
		log.Printf("%s 是导航页节点（%s），直接拿了它报的端口，没扫描，%d 个端口里开着 %d 个", host, node, len(ports), len(open))
	case local:
		open = Run(ctx, host, todo)
		log.Printf("看了本机 %s 的监听表，%d 个端口里在监听的 %d 个，开着 %d 个", host, len(ports), len(todo), len(open))
	default:
		open = Run(ctx, host, todo)
		log.Printf("扫描 %s 的 %d 个端口，开着 %d 个", host, len(ports), len(open))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(struct {
		Scanned int     `json:"scanned"`
		Open    []Found `json:"open"`
		Node    string  `json:"node,omitempty"` // 非空表示这是导航页节点，结果是它自己报的，没扫描
	}{len(ports), open, node})
}

// Ask 是「先问节点」的钩子：host 上也跑着导航页的话，直接问它要开着的端口，
// 不去挨个拨。由 peer 包的 Install 接上；nil（没装 peer）时照常扫描。
// ok 为 false 表示不是节点或者没问通，调用方退回扫描。node 是节点报的名字。
var Ask func(ctx context.Context, host string, ports []int) (open []Found, node string, ok bool)

func askNode(ctx context.Context, host string, ports []int) ([]Found, string, bool) {
	if Ask == nil {
		return nil, "", false
	}
	return Ask(ctx, host, ports)
}

// Run 扫 host 上的这些端口，返回开着的那些，按端口排好。ctx 取消时半路收手。
func Run(ctx context.Context, host string, ports []int) []Found {
	// 结果按下标写回原位，所以不用加锁，出来也天然是按端口排好的。
	res := make([]Found, len(ports))
	var wg sync.WaitGroup
	for i, p := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if f, open := probe(ctx, host, p); open {
				res[i] = f
			}
		}()
	}
	wg.Wait()

	open := res[:0] // 关着的端口 Port 还是零值，就地筛掉
	for _, f := range res {
		if f.Port != 0 {
			open = append(open, f)
		}
	}
	return open
}

// probe 探一个端口：先拨 TCP，开着才继续当 HTTP 请求一次。
// 先试 http，不成再试 https —— 内网服务两种都常见，让用户自己选协议太啰嗦了。
func probe(ctx context.Context, host string, port int) (Found, bool) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	c, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return Found{}, false
	}
	c.Close()

	// host 已经过 ValidHost，拼进 URL 里是安全的。
	f := Found{Port: port, URL: "http://" + addr, Hint: hint[port]}
	for _, scheme := range []string{"http://", "https://"} {
		if r, ok := fetch(ctx, scheme+addr); ok {
			f.URL, f.Title, f.Server, f.Status, f.HTTP = scheme+addr, r.title, r.server, r.status, true
			break
		}
	}
	return f, true
}

// client 只跟「同一个协议、同一个 host:端口，只换路径」的跳转：Jellyfin、
// Grafana 这类首页本来就是 302 到 /web/、/login，不跟过去就拿不到名字。
// 换了主机或端口就停下 —— 那边的标题不是这个端口的；换了协议也停 ——
// github.com:80 那种「跳去 https 但端口还写着 80」跟过去只会拿明文去敲 TLS，
// 白白把一个 301 变成「探测失败」。真的 https 由 probe 自己再试一遍。
// 证书一律不校验，内网服务基本都是自签名。
var client = &http.Client{
	Timeout: httpTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		o := via[0].URL
		if len(via) >= 3 || req.URL.Scheme != o.Scheme || req.URL.Host != o.Host {
			return http.ErrUseLastResponse
		}
		return nil
	},
	Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true, // 每个端口只问一句，连接池留着没用
	},
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// tlsHint 是 nginx 这类服务在 HTTPS 端口上收到明文请求时回的那句话。
// 认出来就当这次 http 没成，让 probe 改用 https 再来一次。
var tlsHint = []byte("plain HTTP request was sent to HTTPS port")

type reply struct {
	title, server string
	status        int
}

// fetch 请求一次并把名字挖出来。ok 为 false 表示这个协议不对，换一个再试。
func fetch(ctx context.Context, u string) (reply, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return reply{}, false
	}
	res, err := client.Do(req)
	if err != nil {
		return reply{}, false // 多半是拿明文去敲 TLS 端口，或者压根不是 HTTP
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10)) // 标题在 <head> 里，64KB 绰绰有余
	if res.StatusCode == http.StatusBadRequest && bytes.Contains(body, tlsHint) {
		return reply{}, false
	}

	r := reply{server: res.Header.Get("Server"), status: res.StatusCode}
	if m := titleRe.FindSubmatch(body); m != nil {
		// 折叠掉换行和缩进：模板里的 <title> 常常是断成好几行写的。
		t := strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
		if utf8.ValidString(t) { // 老设备常回 GBK，乱码还不如不显示
			r.title = trunc(t, 80)
		}
	}
	return r, true
}

func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ParsePorts 解析 "80,443,8000-8100" 这样的写法。去重、排序，
// 超过 portLimit 直接拒绝：一次回一整包 JSON 的前提就是这轮扫得完。
func ParsePorts(s string) ([]int, error) {
	seen := map[int]bool{}
	var ports []int
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := port1(lo)
		if err != nil {
			return nil, err
		}
		b := a
		if isRange {
			if b, err = port1(hi); err != nil {
				return nil, err
			}
		}
		if b < a {
			a, b = b, a // 写反了就当写对了，没必要为这个报错
		}
		for p := a; p <= b; p++ {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
		if len(ports) > portLimit {
			return nil, fmt.Errorf("一次最多扫 %d 个端口，范围再缩小一点", portLimit)
		}
	}
	if len(ports) == 0 {
		return nil, errors.New("还没填端口。可以写 80,443 或者 8000-8100")
	}
	sort.Ints(ports)
	return ports, nil
}

func port1(s string) (int, error) {
	s = strings.TrimSpace(s)
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("端口 %q 不合法：只能是 1-65535 的数字", s)
	}
	return n, nil
}

// ErrHost 是地址不合法时回给用户的话，编辑页的对话框和扫描设置共用。
var ErrHost = errors.New("主机地址不合法：填 IP（192.168.1.10）或主机名（nas.local），不要带 http:// 和路径")

// ValidHost 把地址限制成 IP 或主机名。要挡的是 / ? # @ 和空白 ——
// 它们会让上面拼出来的 URL 指到别的地方去。
func ValidHost(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	if strings.Contains(s, ":") {
		return net.ParseIP(s) != nil // 带冒号只可能是 IPv6，那就必须真的解析得出来
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// LocalIP 猜这台机器在局域网里的地址。两处在用：编辑页那个地址框的 placeholder
// （要扫的多半就是本机或者同网段的邻居，省得去翻 ipconfig），以及判断「这回扫的
// 是不是本机」—— 是的话扫出来的书签存成 ":端口"，跟着当前地址走。
// 认不出来时返回空串，宁可没有，也不要编一个。
//
// 办法是往外「拨」一个 UDP —— UDP 的 connect 不发任何包，只是让系统按默认路由
// 挑一张网卡、绑一个本地地址，拨完就关。地址用 192.0.2.1（RFC 5737 留给文档用的
// 网段，不会是真机器），只是给路由表一个目标，不会真去连谁。
//
// 为什么不直接翻网卡列表：一台机器上 VPN、WSL、Docker、蓝牙各占一张，
// 列表里第一个「看着像内网」的常常不是真正上网那张。按默认路由挑不会认错。
func LocalIP() string {
	if c, err := net.Dial("udp", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsUnspecified() {
			return a.IP.String()
		}
	}
	// 压根没有默认路由（离线、只剩回环）就退回翻网卡，挑第一个还活着的私网 IPv4。
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				return n.IP.String()
			}
		}
	}
	// 实在认不出来就明说「不知道」，不要编一个看着像模像样的 IP 回去：
	// 编辑页拿这个值判断「扫的是不是本机」，编出来的会被当真。
	// placeholder 里那个格式示例由 admin.js 兜底。
	return ""
}
