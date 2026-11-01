// Package peer 是「节点」功能：内网里另一台机器也跑着这个导航页时，不扫它，
// 直接问它的 GET /api/ports，它读自己的监听表把开着的服务报回来。
// 又快又全，也不会一口气拨几千个端口。问不通才退回扫描。
//
// 整个功能都在这个包里，别处只有几行接线：
//
//	main.go          peer.Install、peer.Routes、peer.Listen（-peer 参数）
//	scan 包          scan.Ask 钩子：nil 时照常扫描
//	server 包        卡片描述里的「（导航页节点）」
//	主题             admin.html 里「查找节点」那一块、peer.js、admin.css 末尾一节、
//	                 admin.js 扫描对话框里认 node 的那一行
//
// 不想要了就删掉这个包和上面那几行，其余功能不受影响。
//
// 文件分工：
//
//	node.go   回包格式、问一台机器（客户端）
//	api.go    GET /api/ports（服务端），以及编辑页用的查找接口
//	udp.go    UDP 广播：应答和发问
//	sweep.go  逐个询问网段里的 IP（只在用户点按钮时做）
package peer

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lannav/internal/scan"
)

// NodePorts 是问一台机器「你是不是节点」时依次去试的端口，第一个答得上来的就用。
// 改策略只改这一行：问保存的地址、扫描对话框、「逐个询问网段」都读它。
//
//	7865  导航页的默认端口
//	80    导航页放在反向代理后面、或者直接 -port 80 跑的
//
// 不试 443：内网节点都是明文 HTTP，试了只是对着别的网站白敲一次 TLS。
var NodePorts = []int{7865, 80}

// 问节点的时限。内网里活着的机器几毫秒就回话，关着的端口立刻被拒绝；
// 只有「这个 IP 上压根没机器」才会等满 nodeDial —— 逐个询问网段时大部分 IP 都是这样。
const (
	nodeDial    = 500 * time.Millisecond
	nodeTimeout = 1500 * time.Millisecond
)

// app 和 v 是回包里的暗号：对上了才算是本项目的节点。
// 80 端口上多半是别的网站，它回什么都不会恰好带着这两个字段。
const (
	app = "lannav"
	ver = 1
)

// Report 是 GET /api/ports 的回包。
type Report struct {
	App   string     `json:"app"`
	V     int        `json:"v"`
	Name  string     `json:"name"` // 节点的主机名，页面上显示成「NAS · 192.168.1.10」
	At    time.Time  `json:"at"`
	Ports []NodePort `json:"ports"`
}

// NodePort 是节点上开着的一个端口。跟 scan.Found 差不多，只是不带完整 URL：
// 节点不知道问它的人是用哪个地址连过来的，链接由问的这一方用自己连它的地址拼。
type NodePort struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme,omitempty"` // http / https，不是网页时为空
	Title  string `json:"title,omitempty"`
	Server string `json:"server,omitempty"`
	Status int    `json:"status,omitempty"`
	Hint   string `json:"hint,omitempty"`
	HTTP   bool   `json:"http"`
}

// toNode 把本机扫出来的结果翻成回包里的样子：URL 里只留协议。
func toNode(f scan.Found) NodePort {
	p := NodePort{Port: f.Port, Title: f.Title, Server: f.Server, Status: f.Status, Hint: f.Hint, HTTP: f.HTTP}
	if f.HTTP {
		p.Scheme, _, _ = strings.Cut(f.URL, "://")
	}
	return p
}

// found 把节点报来的端口按「我连它用的地址」拼回 scan.Found，跟扫出来的结果一个样。
func (p NodePort) found(host string) scan.Found {
	scheme := p.Scheme
	if scheme != "https" {
		scheme = "http" // 不是网页的端口也给个 URL，跟 scan.probe 的做法一致
	}
	return scan.Found{
		Port: p.Port, URL: scheme + "://" + net.JoinHostPort(host, strconv.Itoa(p.Port)),
		Title: p.Title, Server: p.Server, Status: p.Status, Hint: p.Hint, HTTP: p.HTTP,
	}
}

// pick 从节点报来的端口里挑出 ports 里有的那些。扫描设置写了端口范围，
// 问节点拿到的也只显示这个范围，跟扫出来的保持一致。节点报来时已经按端口排好。
func (r *Report) pick(host string, ports []int) []scan.Found {
	want := make(map[int]bool, len(ports))
	for _, p := range ports {
		want[p] = true
	}
	out := []scan.Found{}
	for _, p := range r.Ports {
		if want[p.Port] {
			out = append(out, p.found(host))
		}
	}
	return out
}

// Install 把「先问节点」接进 scan 包：之后扫描对话框和「发现的服务」
// 遇到不是本机的地址，都先问一句，是节点就不扫了。
func Install() {
	scan.Ask = func(ctx context.Context, host string, ports []int) ([]scan.Found, string, bool) {
		r, _, ok := Ask(ctx, host)
		if !ok {
			return nil, "", false
		}
		name := r.Name
		if name == "" {
			name = "节点" // 空名字在 scan 那边会被当成「不是节点」
		}
		return r.pick(host, ports), name, true
	}
}

// client 不跟跳转：节点的 /api/ports 直接回 JSON，跳走了就说明不是节点。
var client = &http.Client{
	Timeout:       nodeTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		DialContext:       (&net.Dialer{Timeout: nodeDial}).DialContext,
		DisableKeepAlives: true,
	},
}

// Ask 按 NodePorts 的顺序问 host 是不是节点。是就返回它的回包和答话的那个端口。
// host 应当已经过 scan.ValidHost。
func Ask(ctx context.Context, host string) (*Report, int, bool) {
	for _, port := range NodePorts {
		if r, ok := ask(ctx, host, port); ok {
			return r, port, true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, 0, false
}

func ask(ctx context.Context, host string, port int) (*Report, bool) {
	u := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/api/ports"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, false
	}
	return decode(res.Body)
}

// decode 读回包并对暗号。1MB 足够装下几千个端口；别的网站回一个大页面也不会被整个读进来。
func decode(body io.Reader) (*Report, bool) {
	var r Report
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&r); err != nil {
		return nil, false
	}
	if r.App != app || r.V != ver {
		return nil, false
	}
	return &r, true
}
