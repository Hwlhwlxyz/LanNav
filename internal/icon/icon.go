// Package icon 把书签网站的图标抓回来存在本地，导航页只从本站读图标。
//
// 为什么不在浏览器里直接引用 https://站点/favicon.ico：那样每打开一次导航页，
// 列表里每个网站都会收到访客的 IP 和访问时间，等于把整份书签挨个通知一遍。
// 第三方图标服务（Google s2 之类）更糟，整份书签连同内网地址都交给了它。
// 所以只在管理员点「抓取图标」时由服务端去抓一次，存成文件，之后谁打开都不再往外发请求。
//
// 文件名是 Key 的哈希，导航页按同样的算法查有没有图标，不需要索引文件。
package icon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Key 是一条书签对应的图标的键，空串表示这条书签不取图标（mailto:、ssh:、/path 之类）。
//
// 绝对地址按源（协议 + 主机 + 端口）算，同一个网站的几条书签共用一个图标。
// ":8080" 那种跟着当前地址走的保持原样：从本机和从手机打开时它补出来的主机不一样，
// 按补全后的地址算就会在一边抓到、另一边查不到。
func Key(raw string) string {
	if strings.HasPrefix(raw, ":") {
		p, _, _ := strings.Cut(raw, "/")
		return p
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// Name 是 Key 对应的文件名（不含扩展名）。
func Name(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// FileRe 认图标文件名：只有这些才会被 /icons/ 发出去，挡住路径穿越和别的文件。
var FileRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|ico|svg|gif|jpg|webp)$`)

// Index 读一遍图标目录，返回「文件名去掉扩展名 → 文件名」。目录不存在就是空表。
func Index(dir string) map[string]string {
	m := map[string]string{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if n := e.Name(); FileRe.MatchString(n) {
			m[n[:16]] = n
		}
	}
	return m
}

const (
	maxPage = 256 << 10 // 只为找 <head> 里的 <link rel=icon>
	maxIcon = 512 << 10
)

// client 发的是一个干净的请求：不带 Cookie（没有 CookieJar）、不带 Referer
// （跳转时 Go 会自己加，这里删掉），UA 也不暴露是谁在抓。
// 证书不校验：内网服务基本都是自签名，最坏也就是拿错一张图标。
var client = &http.Client{
	Timeout: 5 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		req.Header.Del("Referer")
		return nil
	},
	Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	},
}

// Fetch 抓 page 这个网页的图标，存成 dir/Name(key).扩展名，同名旧文件一并替换。
// 先看网页里声明的 <link rel="icon">，没有或者抓不到再试 /favicon.ico。
func Fetch(ctx context.Context, dir, key, page string) error {
	base, err := url.Parse(page)
	if err != nil {
		return err
	}
	// 网页超过 maxPage 只看前面那截：<link rel=icon> 都在 <head> 里。
	var cands []string
	body, final, err := get(ctx, page, maxPage)
	var down unreachable
	if errors.As(err, &down) {
		return err // 连都连不上，/favicon.ico 也不用试了，省一轮超时
	}
	if err == nil {
		cands = links(body, final)
	}
	cands = append(cands, base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String())

	// 错误是给编辑页上的人看的：说清楚发生了什么，不贴 Go 的原始报错。
	var last error = errors.New("网页里没声明图标，/favicon.ico 也没有")
	tried := map[string]bool{}
	for _, c := range cands {
		if tried[c] {
			continue
		}
		tried[c] = true
		b, _, err := get(ctx, c, maxIcon)
		if err == nil && len(b) > maxIcon {
			err = fmt.Errorf("图标文件超过 %dKB，没要：%s", maxIcon>>10, c)
		}
		if err != nil {
			last = err
			continue
		}
		ext := sniff(b)
		if ext == "" {
			last = fmt.Errorf("拿到的不是图片：%s", c)
			continue
		}
		return save(dir, Name(key), ext, b)
	}
	return last
}

// get 最多读 limit+1 字节：读满了说明还有没读完的，太大不太大由调用方判断。
func get(ctx context.Context, u string, limit int64) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "*/*")
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, unreachable{err}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("网站没有提供图标（HTTP %d）：%s", res.StatusCode, u)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	return b, res.Request.URL, nil // 跟过跳转的话，相对路径要按最终地址补
}

// unreachable 是请求根本没得到回应（连不上、超时）。网页 404 之类不算 ——
// 那种 /favicon.ico 可能照样在。
type unreachable struct{ error }

func (e unreachable) Unwrap() error { return e.error }

func (e unreachable) Error() string {
	var ne net.Error
	if errors.As(e.error, &ne) && ne.Timeout() || errors.Is(e.error, context.DeadlineExceeded) {
		return fmt.Sprintf("%d 秒内没有响应：地址不对，或者那台机器没开", int(client.Timeout.Seconds()))
	}
	var dns *net.DNSError
	if errors.As(e.error, &dns) {
		return "找不到这个域名"
	}
	return "连不上：" + e.error.Error()
}

var (
	linkRe = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	relRe  = regexp.MustCompile(`(?is)\brel\s*=\s*["']?([^"'>]*)`)
	hrefRe = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

// links 从网页里挑出声明的图标地址：rel 里带 icon 的排前面，apple-touch-icon 垫后
// （它通常是 180px 的大图，能用，但不如专门的 favicon 合适）。
func links(body []byte, base *url.URL) []string {
	var icons, touch []string
	for _, tag := range linkRe.FindAll(body, -1) {
		rel := relRe.FindSubmatch(tag)
		href := hrefRe.FindSubmatch(tag)
		if rel == nil || href == nil {
			continue
		}
		r := strings.ToLower(string(rel[1]))
		h := strings.TrimSpace(html.UnescapeString(string(bytes.Join(href[1:], nil))))
		if h == "" || strings.HasPrefix(h, "data:") {
			continue
		}
		u, err := base.Parse(h)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		switch {
		case strings.Contains(r, "apple-touch-icon"):
			touch = append(touch, u.String())
		case strings.Contains(r, "icon"):
			icons = append(icons, u.String())
		}
	}
	return append(icons, touch...)
}

// sniff 按文件头认图片格式，扩展名由它定，不信 URL 也不信 Content-Type ——
// /favicon.ico 返回 200 加一张 HTML 错误页是很常见的。
func sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return "ico"
	case bytes.HasPrefix(b, []byte("GIF8")):
		return "gif"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg"
	case len(b) > 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WEBP":
		return "webp"
	}
	head := bytes.ToLower(b[:min(len(b), 1024)])
	if bytes.Contains(head, []byte("<svg")) && !bytes.Contains(head, []byte("<html")) {
		return "svg"
	}
	return ""
}

// save 先写临时文件再改名，并删掉同一个图标的其他格式（网站从 ico 换成了 png 之类）。
func save(dir, name, ext string, b []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, name+"."+ext)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, name+".*"))
	for _, f := range old {
		if f != dst {
			os.Remove(f)
		}
	}
	return nil
}

// Prune 删掉 keep 里没有的图标：书签删了，它的图标也不该留着。
func Prune(dir string, keep map[string]bool) {
	for name, file := range Index(dir) {
		if !keep[name] {
			os.Remove(filepath.Join(dir, file))
		}
	}
}
