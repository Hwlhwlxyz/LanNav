package peer

// 这里的测试都不拨真实端口、不发广播：一启动就批量连端口会被 Windows Defender 报毒，
// 测试也一样。只测格式、解析和那几个纯函数；要连的只有 httptest 在回环上起的那一个。

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"lannav/internal/listen"
	"lannav/internal/scan"
)

func TestPortsOutput(t *testing.T) {
	mu.Lock()
	cached = &Report{App: app, V: ver, Name: "NAS", At: time.Now(), Ports: []NodePort{
		{Port: 22, Hint: "SSH"},
		{Port: 8096, Scheme: "http", Title: "Jellyfin", Server: "Kestrel", Status: 200, HTTP: true},
	}}
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); cached = nil; mu.Unlock() })

	// 跟 main 一样挂在一个已经有 "/" 的 mux 上，顺带确认路由不冲突
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	Routes(mux, true)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/ports", nil))
	if w.Code != 200 {
		t.Fatalf("状态码 %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["app"] != "lannav" || got["v"] != 1.0 || got["name"] != "NAS" {
		t.Fatalf("暗号不对：%v", got)
	}
	ps := got["ports"].([]any)
	ssh := ps[0].(map[string]any)
	if _, ok := ssh["scheme"]; ok || ssh["http"] != false {
		t.Fatalf("不是网页的端口不该带 scheme：%v", ssh)
	}
	if _, ok := ps[1].(map[string]any)["url"]; ok {
		t.Fatal("回包里不该有完整 URL")
	}

	// -peer=false：不回答
	mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	Routes(mux, false)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/ports", nil))
	if w.Code != 404 {
		t.Fatalf("-peer=false 时 /api/ports 应该 404，得到 %d", w.Code)
	}
}

// 对方等不及挂断了，后台那一轮也得完整探完存下来，不能存一份残缺的。
func TestReportDetached(t *testing.T) {
	release := make(chan struct{})
	old := probe
	probe = func() (*Report, error) {
		<-release
		return &Report{App: app, V: ver, At: time.Now(), Ports: []NodePort{{Port: 80}, {Port: 8096}}}, nil
	}
	t.Cleanup(func() { probe = old; mu.Lock(); cached = nil; mu.Unlock() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := report(ctx); err == nil {
		t.Fatal("还没探出来、请求又取消了，应该报错")
	}
	mu.Lock()
	d := done
	mu.Unlock()
	close(release)
	<-d

	r, err := report(context.Background())
	if err != nil || len(r.Ports) != 2 {
		t.Fatalf("后台那一轮应该完整存下：%v %v", r, err)
	}
}

func TestDecode(t *testing.T) {
	for _, c := range []struct {
		body string
		ok   bool
	}{
		{`{"app":"lannav","v":1,"name":"NAS","ports":[]}`, true},
		{`{"app":"lannav","v":2,"ports":[]}`, false}, // 格式版本不认识
		{`{"app":"other","v":1}`, false},
		{`<html><title>nginx</title></html>`, false}, // 80 端口上的别的网站
		{``, false},
	} {
		if _, ok := decode(strings.NewReader(c.body)); ok != c.ok {
			t.Errorf("decode(%q) = %v，想要 %v", c.body, ok, c.ok)
		}
	}
}

func TestAsk(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ports" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"app":"lannav","v":1,"name":"NAS","ports":[{"port":22,"http":false},{"port":8096,"scheme":"https","http":true}]}`))
	}))
	defer ts.Close()
	_, p, _ := net.SplitHostPort(ts.Listener.Addr().String())
	port, _ := strconv.Atoi(p)

	old := NodePorts
	NodePorts = []int{port}
	t.Cleanup(func() { NodePorts = old })

	r, got, ok := Ask(context.Background(), "127.0.0.1")
	if !ok || got != port || r.Name != "NAS" {
		t.Fatalf("Ask = %v %d %v", r, got, ok)
	}
	fs := r.pick("10.0.0.9", []int{80, 8096})
	if len(fs) != 1 || fs[0].URL != "https://10.0.0.9:8096" || !fs[0].HTTP {
		t.Fatalf("pick 只该留下 8096，链接用问它的地址拼：%+v", fs)
	}
}

func TestToNode(t *testing.T) {
	n := toNode(scan.Found{Port: 8443, URL: "https://127.0.0.1:8443", Title: "x", HTTP: true})
	if n.Scheme != "https" {
		t.Fatalf("scheme = %q", n.Scheme)
	}
	if n := toNode(scan.Found{Port: 22, URL: "http://127.0.0.1:22", Hint: "SSH"}); n.Scheme != "" {
		t.Fatalf("不是网页的不该带 scheme：%q", n.Scheme)
	}
	if f := (NodePort{Port: 8080, Scheme: "http", HTTP: true}).found("fe80::1"); f.URL != "http://[fe80::1]:8080" {
		t.Fatalf("IPv6 得带方括号：%s", f.URL)
	}
}

func TestTargets(t *testing.T) {
	a := netip.MustParseAddr
	got := targets([]listen.Port{
		{Addr: a("0.0.0.0"), Port: 80},
		{Addr: a("::"), Port: 80},          // 同一个端口只探一次
		{Addr: a("127.0.0.1"), Port: 5432}, // 只绑回环，别的机器连不上，不报
		{Addr: a("192.168.1.10"), Port: 9000},
		{Addr: a("::"), Port: 3000},
	})
	want := map[string][]int{"127.0.0.1": {80}, "::1": {3000}, "192.168.1.10": {9000}}
	if len(got) != len(want) {
		t.Fatalf("targets = %v", got)
	}
	for h, ps := range want {
		if !slices.Equal(got[h], ps) {
			t.Fatalf("targets[%s] = %v，想要 %v", h, got[h], ps)
		}
	}
}

func TestHello(t *testing.T) {
	b, _ := json.Marshal(hello{App: app, V: ver, Q: "who"})
	if string(b) != `{"app":"lannav","v":1,"q":"who"}` {
		t.Fatalf("问话 = %s", b)
	}
	h, ok := parse([]byte(`{"app":"lannav","v":1,"name":"NAS","port":7865}`))
	if !ok || h.Name != "NAS" || h.Port != 7865 || h.Q != "" {
		t.Fatalf("答话解析 = %+v %v", h, ok)
	}
	for _, s := range []string{`{"app":"x","v":1,"q":"who"}`, `{"app":"lannav","v":9}`, `garbage`} {
		if _, ok := parse([]byte(s)); ok {
			t.Errorf("%s 不该认", s)
		}
	}
}

func TestBcastAndAccept(t *testing.T) {
	_, n, _ := net.ParseCIDR("192.168.1.23/24")
	n.IP = net.ParseIP("192.168.1.23").To4() // subnets() 里存的是本机地址，不是网段号
	if b := bcast(n); !b.Equal(net.ParseIP("192.168.1.255")) {
		t.Fatalf("bcast = %s", b)
	}
	_, n2, _ := net.ParseCIDR("10.0.0.0/20")
	if b := bcast(n2); !b.Equal(net.ParseIP("10.0.15.255")) {
		t.Fatalf("bcast /20 = %s", b)
	}

	nets := []*net.IPNet{n}
	for ip, want := range map[string]bool{
		"192.168.1.50": true,
		"192.168.1.23": false, // 本机自己的应答
		"172.17.0.2":   false, // Docker 网段，没往那发过
	} {
		if got := accept(net.ParseIP(ip), nets); got != want {
			t.Errorf("accept(%s) = %v，想要 %v", ip, got, want)
		}
	}
}

func TestNeighbors(t *testing.T) {
	hs := neighbors(netip.MustParseAddr("192.168.1.23"))
	if len(hs) != 253 || hs[0] != "192.168.1.1" || hs[len(hs)-1] != "192.168.1.254" || slices.Contains(hs, "192.168.1.23") {
		t.Fatalf("neighbors: %d 个，首 %s 尾 %s", len(hs), hs[0], hs[len(hs)-1])
	}
}
