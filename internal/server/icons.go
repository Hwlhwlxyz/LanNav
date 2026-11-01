package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lannav/internal/icon"
	"lannav/internal/store"
)

// ——— 网站图标 ———
//
// 编辑页点「抓取图标」才会往外发请求，抓回来存在 data.json 旁边的 icons/ 里；
// 导航页只引用 /icons/…，打开导航页的人不会跟任何书签网站产生联系。
// 图标不进 data.json：抓取不改它，编辑页手里的指纹也就不会失效。

// iconDir 跟着 -data 走：换一份数据文件，图标也跟着换一套。
func (s *Server) iconDir() string {
	return filepath.Join(filepath.Dir(s.Store.Path), "icons")
}

// favicons 在全站开了「显示网站图标」时，给每张卡片标上模式和图标地址。
// 必须在 resolve 之前调：":8080" 补成完整地址后就算不出原来的键了。
func favicons(items []store.Item, idx map[string]string) {
	for i := range items {
		it := &items[i]
		it.Favicons = true
		if k := icon.Key(it.URL); k != "" {
			if f, ok := idx[icon.Name(k)]; ok {
				it.IconSrc = "/icons/" + f
			}
		}
	}
}

// iconFile 处理 GET /icons/{name}。文件名先过一遍白名单，目录本身不给列。
// SVG 里可以带脚本：<img> 引用时不会执行，但有人直接打开这个地址就会，
// 所以加上 CSP 把脚本全关掉。
func (s *Server) iconFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !icon.FileRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache") // 重新抓过之后要能看到新的，靠 Last-Modified 省流量
	if strings.HasSuffix(name, ".ico") {
		h.Set("Content-Type", "image/x-icon") // 有的系统 mime 表里没有 .ico
	}
	http.ServeFile(w, r, filepath.Join(s.iconDir(), name))
}

// iconSite 是编辑页「网站图标」面板里的一行：一个网站（同源的几条书签算一个）。
// Src 有值就是已经抓到了；Error 只在刚抓过、这回没抓到时才有。
type iconSite struct {
	Name  string `json:"name"`
	URL   string `json:"url"` // 去抓的那个地址，":8080" 已经补成本机
	Src   string `json:"src,omitempty"`
	Error string `json:"error,omitempty"`

	key string
}

// iconSites 按书签顺序列出要取图标的网站（含隐藏的书签），同一个网站只列一次。
func iconSites(d *store.Data, idx map[string]string) []iconSite {
	var sites []iconSite
	seen := map[string]bool{}
	for _, g := range d.Groups {
		for _, it := range g.Items {
			k := icon.Key(it.URL)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			page := it.URL
			if strings.HasPrefix(page, ":") {
				page = "http://127.0.0.1" + page // 跟着当前地址走的，指的就是跑着导航页的这台机器
			}
			site := iconSite{Name: it.Name, URL: page, key: k}
			if f, ok := idx[icon.Name(k)]; ok {
				site.Src = "/icons/" + f
			}
			sites = append(sites, site)
		}
	}
	return sites
}

// fetchIcons 处理 POST /api/icons/refresh：把 data.json 里所有书签（含隐藏的）的图标
// 重新抓一遍，同一个网站只抓一次，已经不在书签里的图标顺手删掉。
// 读的是磁盘上的 data.json —— 编辑页还没保存的改动不算，前端会先拦一道。
// 回的是抓完之后每个网站的状态，编辑页拿它整个替换掉那张列表。
func (s *Server) fetchIcons(w http.ResponseWriter, r *http.Request) {
	_, d, _, err := s.Store.Raw()
	if err != nil {
		http.Error(w, "读取数据失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	sites := iconSites(d, nil)

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	dir := s.iconDir()
	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, 8)
	)
	for i := range sites {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := icon.Fetch(ctx, dir, sites[i].key, sites[i].URL); err != nil {
				sites[i].Error = err.Error() // 每个 goroutine 只写自己那一格，不用加锁
			}
		})
	}
	wg.Wait()
	keep := map[string]bool{}
	for _, site := range sites {
		keep[icon.Name(site.key)] = true
	}
	if ctx.Err() == nil {
		icon.Prune(dir, keep) // 中途超时或断开就别删了，留着旧的总比全没了强
	}

	// 抓完重新查一遍目录：没抓到但以前抓到过的，图标还在，照样显示。
	// 地址带上时间戳，浏览器才会换掉页面上那张旧图。
	idx := icon.Index(dir)
	v := strconv.FormatInt(time.Now().Unix(), 10)
	failed := 0
	for i := range sites {
		if f, ok := idx[icon.Name(sites[i].key)]; ok {
			sites[i].Src = "/icons/" + f + "?v=" + v
		}
		if sites[i].Error != "" {
			failed++
		}
	}
	log.Printf("抓取图标：%d 个网站，%d 个没抓到", len(sites), failed)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{"sites": sites})
}
