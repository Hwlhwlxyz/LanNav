package server

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"

	"lannav/internal/icon"
	"lannav/internal/scan"
	"lannav/internal/store"
)

// ——— 编辑页 ———
//
// 编辑页只做两件事：GET /admin 把当前 data.json 摊成一屏输入框，
// POST /api/data 收下整份文档写回磁盘。没有单条增删接口 —— 文件才几十 KB，
// 整份提交省掉一整套 id 管理和并发合并逻辑。

// adminPage 渲染编辑页。用的是原始数据（含 hidden 条目），
// 并把文件指纹塞进 body 的 data-stamp，提交时原样带回来。
func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	_, d, stamp, err := s.Store.Raw()
	if err != nil {
		http.Error(w, "读取数据失败："+err.Error(), http.StatusInternalServerError)
		log.Println("admin load:", err)
		return
	}
	// newItem/newGroup 让模板里的「空白行」和「已有行」共用同一段 {{define}}，
	// 行的结构只写一次，不会出现模板和 JS 各写一份、改一处漏一处。
	tmpl, err := template.New("admin.html").Funcs(template.FuncMap{
		"newItem":  func() store.Item { return store.Item{} },
		"newGroup": func() store.Group { return store.Group{} },
		"inc":      func(n int) int { return n + 1 },
	}).ParseFS(s.Theme, "admin.html")
	if err != nil {
		http.Error(w, "模板解析失败："+err.Error(), http.StatusInternalServerError)
		log.Println("admin template:", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// LocalIP 只是扫描对话框那个地址框的 placeholder，见 scan 包。
	if err := tmpl.Execute(w, struct {
		*store.Data
		Stamp   string
		LocalIP string
		Icons   []iconSite // 「网站图标」面板的列表
	}{d, stamp, scan.LocalIP(), iconSites(d, icon.Index(s.iconDir()))}); err != nil {
		log.Println("admin execute:", err)
	}
}

// save 处理 POST /api/data：校验 → 交给 store 写盘。
func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Stamp string     `json:"stamp"`
		Data  store.Data `json:"data"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "请求格式错误："+err.Error(), http.StatusBadRequest)
		return
	}
	if err := store.Normalize(&req.Data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := checkScan(req.Data.Scan); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stamp, err := s.Store.Save(&req.Data, req.Stamp)
	if errors.Is(err, store.ErrConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("已保存 %s（%d 个分组）", s.Store.Path, len(req.Data.Groups))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]string{"stamp": stamp})
}
