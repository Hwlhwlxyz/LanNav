package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Normalize 校验并规整。校验不过整份拒绝，绝不写入半份数据。
// order 一律按提交顺序重排 —— 页面上条目的先后就是唯一的真相，
// 客户端不用管这个字段，手改过的 data.json 里那些跳号、重号也顺手被抹平。
func Normalize(d *Data) error {
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		return errors.New("站点标题不能为空")
	}
	if len(d.Groups) == 0 {
		return errors.New("至少要保留一个分组")
	}
	if !validOpen(d.Open) {
		return fmt.Errorf("链接打开方式 %q 不认识，只能是 %q 或 %q", d.Open, OpenTab, OpenSelf)
	}
	seen := map[string]bool{}
	for i := range d.Groups {
		g := &d.Groups[i]
		g.ID = strings.TrimSpace(g.ID)
		g.Name = strings.TrimSpace(g.Name)
		g.Icon = strings.TrimSpace(g.Icon)
		if !validID(g.ID) {
			return fmt.Errorf("第 %d 个分组的 id %q 不合法：只能用字母、数字、连字符和下划线（它要进 URL）", i+1, g.ID)
		}
		if g.ID == ServicesID {
			return fmt.Errorf("分组 id %q 被「发现的服务」占用了，换一个", g.ID)
		}
		if seen[g.ID] {
			return fmt.Errorf("分组 id %q 重复了，每个分组的 id 必须唯一", g.ID)
		}
		seen[g.ID] = true
		if g.Name == "" {
			return fmt.Errorf("分组 %q 的名称不能为空", g.ID)
		}
		g.Order = i + 1
		for j := range g.Items {
			it := &g.Items[j]
			it.Name = strings.TrimSpace(it.Name)
			it.URL = strings.TrimSpace(it.URL)
			it.Description = strings.TrimSpace(it.Description)
			it.Icon = strings.TrimSpace(it.Icon)
			if it.Name == "" {
				return fmt.Errorf("分组「%s」第 %d 条书签还没填名称", g.Name, j+1)
			}
			if it.URL == "" {
				return fmt.Errorf("分组「%s」的「%s」还没填链接", g.Name, it.Name)
			}
			// 冒号开头是「跟着当前地址走」的写法，后面必须是端口号。
			// 这里拦住，是因为 ":abc" 拼出来的地址点不开，而且模板会把它当成
			// 可疑 URL 换成 #ZgotmplZ —— 与其存进去变成死链，不如当场退回。
			if strings.HasPrefix(it.URL, ":") && !RelPort(it.URL) {
				return fmt.Errorf("分组「%s」的「%s」链接 %q 写法不对：冒号开头表示跟着当前访问的地址走，冒号后面得是端口号，比如 :8080/path", g.Name, it.Name, it.URL)
			}
			if !validOpen(it.Open) {
				return fmt.Errorf("分组「%s」的「%s」打开方式 %q 不认识", g.Name, it.Name, it.Open)
			}
			it.Order = j + 1
		}
	}

	// 扫描设置只在这里规整（去空白、去空行、去重）；地址和端口写得对不对
	// 由 server 拿 scan 包去校验 —— store 不该为了几条校验规则依赖扫描的实现。
	hosts := d.Scan.Hosts[:0]
	dup := map[string]bool{}
	for _, h := range d.Scan.Hosts {
		if h = strings.TrimSpace(h); h != "" && !dup[h] {
			dup[h] = true
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		hosts = nil // 空切片不算零值，omitzero 会留下一个 "scan": {}
	}
	d.Scan.Hosts = hosts
	d.Scan.Ports = strings.TrimSpace(d.Scan.Ports)
	return nil
}

// RelPort 认 ":8080" 和 ":8080/path"：冒号到第一个斜杠之间必须是个端口号。
// 卡住这一条，是为了不去动 "mailto:" 之类本来就合法、也是冒号打头的写法之外的东西，
// 更不要把 ":abc" 拼成一个点不开的地址。
func RelPort(u string) bool {
	if !strings.HasPrefix(u, ":") {
		return false
	}
	p, _, _ := strings.Cut(u[1:], "/")
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// validOpen 认打开方式：空（跟随默认）、新标签页、当前页。
func validOpen(s string) bool {
	return s == "" || s == OpenTab || s == OpenSelf
}

// validID 限制分组 id 只能是 ASCII 字母数字加 - _：它要直接出现在 URL 路径里。
func validID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
