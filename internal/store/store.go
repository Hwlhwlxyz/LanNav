// Package store 管 data.json：读、排序过滤、校验、写回。
//
// 每次调用都重新读文件 —— 改完刷新即生效，不用重启。
// 导航页的 QPS 以「个位数每小时」计，省这点 IO 没有意义。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
)

type Item struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Order       int    `json:"order"`
	Hidden      bool   `json:"hidden,omitempty"`
	Open        string `json:"open,omitempty"` // 空 = 跟随全站的 Data.Open；否则 OpenTab / OpenSelf

	// Target 不存盘，是渲染前由 server 按 Open 和全站设置算好的 <a target>。
	// 模板只管输出，不去琢磨「跟随默认」是什么意思。
	Target string `json:"-"`

	// Favicons 和 IconSrc 也不存盘：全站开了「显示网站图标」时 Favicons 为真，
	// 卡片不再显示 emoji，改显示 IconSrc（抓回来的图标），没抓到就显示默认的地球。
	// 放在每条上而不是只看全站，是因为卡片模板拿到的只有这一条。
	Favicons bool   `json:"-"`
	IconSrc  string `json:"-"`
}

// 链接的打开方式。
const (
	OpenTab  = "tab"  // 新标签页
	OpenSelf = "self" // 当前页直接跳转
)

type Group struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Order  int    `json:"order"`
	Hidden bool   `json:"hidden,omitempty"`
	Items  []Item `json:"items"`
}

type Data struct {
	Title    string     `json:"title"`
	Open     string     `json:"open,omitempty"`     // 全站默认的打开方式，空 = OpenTab
	Favicons bool       `json:"favicons,omitempty"` // 卡片显示网站图标而不是 emoji；默认关，图标由管理员手动抓（internal/icon）
	Groups   []Group    `json:"groups"`
	Scan     ScanConfig `json:"scan,omitzero"`
}

// TargetOf 算出一条链接最终的 <a target>：单条设了就听单条的，没设听全站的，
// 全站也没设就新标签页 —— 这是加这个选项之前的老行为，旧的 data.json 打开不变样。
func (d *Data) TargetOf(open string) string {
	if open == "" {
		open = d.Open
	}
	if open == OpenSelf {
		return ""
	}
	return "_blank"
}

// ScanConfig 是导航页「发现的服务」要扫的地址和端口，在编辑页里设置。
// Hosts 为空表示没开这个功能，导航页上也就不出现那一栏。
type ScanConfig struct {
	Hosts []string `json:"hosts,omitempty"`
	Ports string   `json:"ports,omitempty"` // 写法同扫描对话框：80,443,8000-8100
}

// ServicesID 是「发现的服务」那一栏在导航页上的分组 id，也就是它的路径 /services。
// 它不是 data.json 里的分组，所以用户的分组不能占用这个 id。
const ServicesID = "services"

// ErrConflict 表示编辑期间 data.json 被别处改过，提交带回来的指纹对不上。
var ErrConflict = errors.New("data.json 在你编辑期间被改动过。请刷新页面重新编辑，以免覆盖掉那边的改动。")

// Store 是一份 data.json。Path 启动时定好，之后不变。
type Store struct {
	Path string
}

// Raw 读原始数据：不过滤 hidden、不排序，编辑页要看到全部条目。
// stamp 是文件指纹（改动时间 + 大小），编辑页带着它提交，
// 用来发现「编辑期间 data.json 被手改过」，避免静默覆盖。
func (s *Store) Raw() (b []byte, d *Data, stamp string, err error) {
	st, err := os.Stat(s.Path)
	if err != nil {
		return nil, nil, "", err
	}
	if b, err = os.ReadFile(s.Path); err != nil {
		return nil, nil, "", err
	}
	d = &Data{}
	if err = json.Unmarshal(b, d); err != nil {
		return nil, nil, "", err
	}
	return b, d, fmt.Sprintf("%d-%d", st.ModTime().UnixNano(), st.Size()), nil
}

// Load 在 Raw 之上把 hidden 过滤掉、按 order 排好序。
// 导航页的模板因此不需要任何 if/sort，纯输出。
func (s *Store) Load() (*Data, error) {
	_, d, _, err := s.Raw()
	if err != nil {
		return nil, err
	}
	groups := d.Groups[:0]
	for _, g := range d.Groups {
		if g.Hidden {
			continue
		}
		items := g.Items[:0]
		for _, it := range g.Items {
			if !it.Hidden {
				items = append(items, it)
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
		g.Items = items
		groups = append(groups, g)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Order < groups[j].Order })
	d.Groups = groups
	return d, nil
}

// Save 写回 data.json，返回新指纹。d 应当已经过 Normalize。
// 顺序是：比对指纹 → 备份 → 临时文件 → rename。
// 指纹对不上返回 ErrConflict。
func (s *Store) Save(d *Data, stamp string) (string, error) {
	old, _, cur, err := s.Raw()
	if err == nil && cur != stamp {
		return "", ErrConflict
	}

	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化失败：%w", err)
	}
	out = append(out, '\n')

	if old != nil {
		if err := os.WriteFile(s.Path+".bak", old, 0o644); err != nil {
			log.Println("backup:", err) // 备份失败不阻断保存，只记一笔
		}
	}
	// 先写临时文件再 rename：rename 在同一分区上是原子的，
	// 中途崩溃最多留下一个 .tmp，不会把 data.json 截成半个 JSON。
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return "", fmt.Errorf("写入失败：%w", err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("替换失败：%w", err)
	}

	_, _, stamp, _ = s.Raw() // 新指纹给页面，让它能接着改
	return stamp, nil
}
