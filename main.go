// 导航页服务端。一个二进制同时是开发服务器和生产服务：
//
//	go run .                      http://localhost:7865，默认主题
//	go run . -theme mytheme       换一套模板/样式
//	go run . -port 9000 -data x.json
//	go run . -bind 127.0.0.1      只给本机用：不监听网卡，Windows 防火墙不会弹窗
//
// main 只负责解析参数、把各部分接起来；逻辑都在 internal/ 下：
//
//	internal/theme   主题从磁盘还是从二进制里读
//	internal/store   data.json 的读取、排序过滤、校验、写回
//	internal/server  路由、导航页、编辑页
//	internal/scan    端口扫描接口
//	internal/peer    节点：别的机器也跑着导航页时直接问它要端口，不扫（可整个删掉，见包注释）
package main

import (
	"embed"
	"flag"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"

	"lannav/internal/peer"
	"lannav/internal/server"
	"lannav/internal/store"
	"lannav/internal/theme"
)

// 主题目录整个编译进二进制，这样分发时只要一个可执行文件加一份 data.json。
// data.json 不嵌 —— 它是运行时配置，嵌进去就失去「改完刷新生效」的意义了。
// go:embed 不能引用上级目录，所以这一行只能留在 themes/ 旁边的 main 包里。
//
//go:embed themes
var builtin embed.FS

func main() {
	name := flag.String("theme", "default", "主题目录名，位于 themes/ 下")
	dataFile := flag.String("data", filepath.Join("data", "data.json"), "数据文件路径")
	port := flag.String("port", "7865", "监听端口")
	bind := flag.String("bind", "", "监听地址，留空为所有网卡；写 127.0.0.1 只给本机用，不触发防火墙弹窗")
	serve := flag.Bool("peer", true, "回答别的导航页的查询和广播；false 时这台机器不被发现、不被查询")
	flag.Parse()
	webPort, err := strconv.Atoi(*port)
	if err != nil {
		log.Fatalf("端口 %q 不是数字", *port)
	}
	// 只听回环地址时别的机器本来就连不上，再去应答 UDP 广播只会把一个打不开的端口报出去，
	// 而且那个 UDP 监听同样会触发防火墙弹窗 —— 这正是用 -bind 想躲开的东西。
	local := *bind == "localhost" || net.ParseIP(*bind).IsLoopback()
	if local && *serve {
		*serve = false
		log.Printf("只监听 %s，关闭节点应答（别的机器连不到这台）", *bind)
	}

	fsys, src, err := theme.Open(builtin, *name)
	if err != nil {
		log.Fatal(err)
	}
	srv := &server.Server{Store: &store.Store{Path: *dataFile}, Theme: fsys}
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())

	// 节点功能的接线全在这几行，不想要了连同 internal/peer 一起删掉。
	peer.Install()           // 扫描前先问对方是不是节点
	peer.Routes(mux, *serve) // /api/ports 和编辑页的「查找节点」
	if *serve {
		peer.Listen(webPort) // 应答 UDP 广播
		peer.Warm()          // 后台先把本机端口探好，别的节点第一次来问就有
	}

	srv.Warm() // 「发现的服务」先在后台看一眼本机、问一下已保存的节点，不等它就开始监听

	log.Printf("主题 %s（%s）· 数据 %s · http://localhost:%s（编辑页 /admin）", *name, src, *dataFile, *port)
	log.Fatal(http.ListenAndServe(net.JoinHostPort(*bind, *port), mux)) // bind 为空时就是 ":7865"，所有网卡
}
