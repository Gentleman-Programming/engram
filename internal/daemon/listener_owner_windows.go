//go:build windows

package daemon

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

var extendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// ListenerOwnerPID returns a unique IPv4 loopback/wildcard LISTEN owner observed
// by the OS, or zero when unavailable/ambiguous. This snapshot is diagnostic
// only: it must never replace pidfile/start-token identity before signaling.
func ListenerOwnerPID(port int) int {
	if port < 1 || port > 65535 || extendedTCPTable.Find() != nil {
		return 0
	}
	var size uint32
	var buffer []byte
	for range 3 {
		var address unsafe.Pointer
		if len(buffer) > 0 {
			address = unsafe.Pointer(&buffer[0])
		}
		// AF_INET=2, TCP_TABLE_OWNER_PID_LISTENER=3. No process is opened.
		status, _, _ := extendedTCPTable.Call(uintptr(address), uintptr(unsafe.Pointer(&size)), 0, 2, 3, 0)
		if status == 0 {
			if size > uint32(len(buffer)) {
				return 0
			}
			return listenerOwnerFromTable(buffer[:size], port)
		}
		if status != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size < 4 || size > 1<<20 {
			return 0
		}
		buffer = make([]byte, size)
	}
	return 0
}

func listenerOwnerFromTable(table []byte, port int) int {
	const rowSize = 24 // MIB_TCPROW_OWNER_PID: six DWORD fields after count.
	if len(table) < 4 {
		return 0
	}
	count := uint64(binary.LittleEndian.Uint32(table))
	if count > uint64((len(table)-4)/rowSize) {
		return 0
	}
	owner := uint32(0)
	for i := uint64(0); i < count; i++ {
		row := table[4+i*rowSize : 4+(i+1)*rowSize]
		address := binary.LittleEndian.Uint32(row[4:8])
		localPort := int(binary.BigEndian.Uint16(row[8:10]))
		if binary.LittleEndian.Uint32(row) != 2 || (address != 0 && address != 0x0100007f) || localPort != port {
			continue
		}
		pid := binary.LittleEndian.Uint32(row[20:24])
		if pid == 0 || pid > maxPid || (owner != 0 && owner != pid) {
			return 0
		}
		owner = pid
	}
	return int(owner)
}
