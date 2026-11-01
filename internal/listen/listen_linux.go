package listen

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// stateListen 是 /proc/net/tcp 里 st 那一列的 LISTEN（内核的 TCP_LISTEN = 10）。
const stateListen = "0A"

// tcp 读 /proc/net/tcp 和 tcp6。只看得到当前网络命名空间：
// 在容器里跑看到的是容器自己的端口，除非用 --network host。
func tcp() ([]Port, error) {
	var ps []Port
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		got, err := readProc(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue // 关了 IPv6 就没有 tcp6
		}
		if err != nil {
			return nil, err
		}
		ps = append(ps, got...)
	}
	return ps, nil
}

// readProc 解析一个 /proc/net/tcp* 文件。每行长这样（第一行是表头）：
//
//	sl  local_address rem_address   st ...
//	0: 0100007F:1F90 00000000:0000 0A ...
func readProc(name string) ([]Port, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var ps []Port
	sc := bufio.NewScanner(f)
	sc.Scan() // 表头
	for sc.Scan() {
		col := strings.Fields(sc.Text())
		if len(col) < 4 || col[3] != stateListen {
			continue
		}
		p, err := parseAddr(col[1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		ps = append(ps, p)
	}
	return ps, sc.Err()
}

// parseAddr 解析 "0100007F:1F90"。端口是大端的十六进制；地址是按 32 位一组、
// 每组以本机字节序打出来的十六进制 —— 所以要一组一组按本机字节序写回去，
// 才是网络序的那几个字节。IPv4 一组，IPv6 四组。
func parseAddr(s string) (Port, error) {
	hexIP, hexPort, ok := strings.Cut(s, ":")
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if !ok || err != nil || (len(hexIP) != 8 && len(hexIP) != 32) {
		return Port{}, fmt.Errorf("看不懂的地址 %q", s)
	}
	raw, err := hex.DecodeString(hexIP)
	if err != nil {
		return Port{}, fmt.Errorf("看不懂的地址 %q", s)
	}
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(raw[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	ip, _ := netip.AddrFromSlice(raw)
	return Port{Addr: ip, Port: int(port)}, nil
}
