// Package server 是导航页的 HTTP 层：路由、导航页、编辑页、保存接口。
//
// 设计上只有三条规则：
//  1. 每次请求都重新读 data.json 和模板 —— 改完刷新即生效，不用重启。
//  2. 所有分组一次性渲染进同一个页面，URL 路径只决定哪个分组带 active 类。
//     这样前端搜索能跨分组命中，切换分组也不需要任何 JS。
//  3. 排序和过滤在 store 里做完，模板只负责输出，不含逻辑。
package server

import (
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"

	"lannav/internal/icon"
	"lannav/internal/scan"
	"lannav/internal/store"
)

// Server 把数据和主题拼成页面。Store 和 Theme 在启动时定好。
type Server struct {
	Store *store.Store
	Theme fs.FS

	scans scan.Cache // 「发现的服务」的扫描结果，只在内存里
}

// Page 是交给导航页模板的全部数据。Active 是当前选中的分组 id。
type Page struct {
	store.Data
	Active   string
	Services *Services // 没设扫描地址时是 nil，侧边栏就不出现那一栏
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)                                                   // 根路径
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(s.Theme))) // 主题里的 css/js
	mux.HandleFunc("GET /admin", s.adminPage)                                            // 编辑页（比 /{id} 更具体，优先匹配）
	mux.HandleFunc("POST /api/data", s.save)                                             // 整份写回 data.json
	mux.HandleFunc("POST /api/scan", scan.Handle)                                        // 扫一个地址上开着的服务
	mux.HandleFunc("POST /api/services/refresh", s.refresh)                              // 重扫「发现的服务」（services.go）
	mux.HandleFunc("POST /api/icons/refresh", s.fetchIcons)                              // 抓取所有书签的网站图标（icons.go）
	mux.HandleFunc("GET /icons/{name}", s.iconFile)                                      // 抓回来的图标
	mux.HandleFunc("GET /{id}", s.page)                                                  // /dev、/tools …
	return mux
}

// page 处理 / 和 /{id}：整页渲染所有分组，只有 Active 不同。
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Load()
	if err != nil {
		http.Error(w, "读取数据失败："+err.Error(), http.StatusInternalServerError)
		log.Println("load:", err)
		return
	}
	if len(d.Groups) == 0 {
		http.Error(w, "data.json 里没有任何可见分组", http.StatusInternalServerError)
		return
	}
	svc := s.services(d.Scan, r)
	if d.Favicons {
		idx := icon.Index(s.iconDir())
		for i := range d.Groups {
			favicons(d.Groups[i].Items, idx)
		}
		if svc != nil {
			favicons(svc.Items, idx)
		}
	}
	for i := range d.Groups {
		resolve(d.Groups[i].Items, r)
		targets(d.Groups[i].Items, d)
	}
	if svc != nil {
		resolve(svc.Items, r)
		targets(svc.Items, d) // 扫出来的没有单条设置，Open 是空的，全跟全站
	}

	active := r.PathValue("id")
	if active == "" {
		active = d.Groups[0].ID // 根路径默认选中第一个分组
	} else if !has(d.Groups, active) && (svc == nil || active != svc.ID) {
		http.NotFound(w, r) // 分组 id 写错时直接 404，不要静默回落到第一个
		return
	}

	// 每次请求重新解析模板：走磁盘时改完 index.html 刷新即生效，不用重启。
	tmpl, err := template.ParseFS(s.Theme, "index.html")
	if err != nil {
		http.Error(w, "模板解析失败："+err.Error(), http.StatusInternalServerError)
		log.Println("template:", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, Page{*d, active, svc}); err != nil {
		log.Println("execute:", err) // 响应已开始写出，只能记日志
	}
}

// resolve 把 ":8080/test" 这种「跟着当前地址走」的链接补成真地址。
// 拿的是这次请求的 Host —— 也就是用户地址栏里主机那一截 —— 所以同一份 data.json，
// 在本机打开点出去是 localhost，掏出手机按 IP 打开点出去就是那个 IP。
//
// 这件事只能在服务端做：每次请求是从哪个地址进来的，只有服务端知道。
// 书签里写死一台机器的 IP，换个网段或者换台机器就点不开了，这个写法就是冲它去的。
//
// 只管冒号开头的。"/path" 不用管 —— 浏览器本来就会拿当前地址去补；
// 绝对地址更不用管。
func resolve(items []store.Item, r *http.Request) {
	host := hostOf(r)
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 得带方括号，不然后面那个 :端口 接不上去
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	for i := range items {
		if it := &items[i]; store.RelPort(it.URL) {
			it.URL = scheme + "://" + host + it.URL
		}
	}
}

// targets 按单条和全站的设置，给每条链接定好是新标签页打开还是当前页跳转。
func targets(items []store.Item, d *store.Data) {
	for i := range items {
		items[i].Target = d.TargetOf(items[i].Open)
	}
}

// hostOf 取这次请求地址栏里主机那一截，不带端口，IPv6 也不带方括号。
func hostOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		// 走的 80/443，Host 里本来就没带端口；IPv6 这时还裹着方括号，剥掉
		host = strings.TrimSuffix(strings.TrimPrefix(r.Host, "["), "]")
	}
	return host
}

func has(groups []store.Group, id string) bool {
	for _, g := range groups {
		if g.ID == id {
			return true
		}
	}
	return false
}
