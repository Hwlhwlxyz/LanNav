// Package theme 决定当前主题从哪读。
//
// 磁盘上有这个主题目录就用磁盘，没有才用内置的那份。开发时（源码目录下 go run）
// 走磁盘，改模板/样式刷新即生效；把编译好的二进制单独拷出去跑，身边没有 themes/，
// 就自动用嵌进去的。
package theme

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// Open 返回主题 name 的文件系统，以及它的来源（「磁盘」或「内置」，打日志用）。
// builtin 是嵌进二进制的 themes 目录的上一层，也就是里面带着 themes/ 前缀。
func Open(builtin fs.FS, name string) (fsys fs.FS, src string, err error) {
	dir := filepath.Join("themes", name)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		return os.DirFS(dir), "磁盘", nil
	}
	sub, err := fs.Sub(builtin, path.Join("themes", name))
	if err != nil {
		return nil, "", fmt.Errorf("主题 %q 不可用：%v", name, err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, "", fmt.Errorf("主题 %q 不可用：磁盘上没有 %s，二进制里也没嵌进去", name, dir)
	}
	return sub, "内置", nil
}
