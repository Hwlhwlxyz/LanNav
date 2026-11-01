package listen

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// GetExtendedTcpTable 的参数，见
// https://learn.microsoft.com/windows/win32/api/iphlpapi/nf-iphlpapi-getextendedtcptable
const (
	afInet                   = 2
	afInet6                  = 23
	tcpTableOwnerPIDListener = 3   // TCP_TABLE_OWNER_PID_LISTENER：只要监听中的。BASIC 那几种不支持 IPv6
	errInsufficientBuffer    = 122 // ERROR_INSUFFICIENT_BUFFER

	// 表里每一行的大小和本地地址、端口所在的偏移（MIB_TCPROW_OWNER_PID / MIB_TCP6ROW_OWNER_PID）。
	// 端口是 DWORD，但只有低 16 位有用，而且是网络序 —— 直接取那两个字节按大端读。
	row4Size, row4Addr, row4Port = 24, 4, 8
	row6Size, row6Addr, row6Port = 56, 0, 20
)

// 写全路径去 System32 里加载，不让当前目录下同名的 dll 冒名顶替。
var procTable = syscall.NewLazyDLL(filepath.Join(os.Getenv("SystemRoot"), "System32", "iphlpapi.dll")).
	NewProc("GetExtendedTcpTable")

func tcp() ([]Port, error) {
	if err := procTable.Find(); err != nil {
		return nil, err
	}
	v4, err := table(afInet, row4Size, row4Addr, 4, row4Port)
	if err != nil {
		return nil, err
	}
	v6, err := table(afInet6, row6Size, row6Addr, 16, row6Port)
	if err != nil {
		return nil, err
	}
	return append(v4, v6...), nil
}

// table 取一张表并解析。表的开头是一个 DWORD 的行数，后面紧跟着一行行定长的记录。
func table(af uint32, rowSize, addrOff, addrLen, portOff int) ([]Port, error) {
	var size uint32
	var buf []byte
	// 先问要多大，再按那么大取。两次调用之间有新的监听冒出来，表会变大，那就再来一次。
	for range 5 {
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procTable.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
		switch {
		case r == errInsufficientBuffer, r == 0 && len(buf) < 4:
			buf = make([]byte, max(size, 4))
			continue
		case r != 0:
			return nil, fmt.Errorf("GetExtendedTcpTable: %w", syscall.Errno(r))
		}

		n := int(binary.LittleEndian.Uint32(buf))
		if 4+n*rowSize > len(buf) {
			return nil, fmt.Errorf("GetExtendedTcpTable: %d 行放不进 %d 字节", n, len(buf))
		}
		ps := make([]Port, 0, n)
		for i := range n {
			row := buf[4+i*rowSize:][:rowSize]
			ip, _ := netip.AddrFromSlice(row[addrOff : addrOff+addrLen])
			ps = append(ps, Port{Addr: ip, Port: int(binary.BigEndian.Uint16(row[portOff:]))})
		}
		return ps, nil
	}
	return nil, fmt.Errorf("GetExtendedTcpTable: 表一直在变，取不下来")
}
