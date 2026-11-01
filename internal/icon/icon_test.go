package icon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var png = []byte("\x89PNG\r\n\x1a\nfake")

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://GitHub.com/a/b":   "https://github.com",
		"http://192.168.1.10:8096": "http://192.168.1.10:8096",
		":8080/path":               ":8080",
		"/relative":                "",
		"mailto:a@b.c":             "",
	} {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

// 网页很大、<link> 写在 <head> 里、属性顺序是 href 在前：照样认出来，
// 不发 Referer，也不去碰 /favicon.ico。
func TestFetchLinkInBigPage(t *testing.T) {
	var hitFavicon bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("%s 带了 Referer/Cookie", r.URL)
		}
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`<html><head><link href="/static/i.png" rel="shortcut icon"></head><body>`))
			w.Write([]byte(strings.Repeat("x", maxPage*2)))
		case "/static/i.png":
			w.Write(png)
		default:
			hitFavicon = true
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	key := Key(srv.URL)
	if err := Fetch(context.Background(), dir, key, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if hitFavicon {
		t.Error("声明了图标还去试 /favicon.ico")
	}
	b, err := os.ReadFile(filepath.Join(dir, Name(key)+".png"))
	if err != nil || string(b) != string(png) {
		t.Fatalf("图标没存对：%v", err)
	}
}

// /favicon.ico 回一张 HTML 错误页不能当图标存下来。
func TestFetchRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>not found</body></html>"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := Fetch(context.Background(), dir, Key(srv.URL), srv.URL); err == nil {
		t.Fatal("HTML 被当成了图标")
	}
	if len(Index(dir)) != 0 {
		t.Fatal("不该存下任何文件")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	a, b := Name("a"), Name("b")
	save(dir, a, "png", png)
	save(dir, b, "ico", []byte{0, 0, 1, 0})
	save(dir, a, "ico", []byte{0, 0, 1, 0}) // 换了格式，旧的 png 要被替掉
	Prune(dir, map[string]bool{a: true})
	idx := Index(dir)
	if len(idx) != 1 || idx[a] != a+".ico" {
		t.Fatalf("剩下 %v", idx)
	}
}
