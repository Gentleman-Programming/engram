//go:build windows

package daemon

import (
	"encoding/binary"
	"net"
	"os"
	"testing"
)

func TestListenerOwnerPIDNative(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "0.0.0.0:0"} {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if pid := ListenerOwnerPID(port); pid != os.Getpid() {
			t.Errorf("owner of %s = %d, want fixture pid %d", address, pid, os.Getpid())
		}
		_ = listener.Close()
		if pid := ListenerOwnerPID(port); pid != 0 {
			t.Errorf("closed fixture owner = %d", pid)
		}
	}
}

func TestListenerOwnerTableValidation(t *testing.T) {
	row := func(address uint32, port uint16, pid uint32) []byte {
		data := make([]byte, 24)
		binary.LittleEndian.PutUint32(data, 2)
		binary.LittleEndian.PutUint32(data[4:], address)
		binary.BigEndian.PutUint16(data[8:], port)
		binary.LittleEndian.PutUint32(data[20:], pid)
		return data
	}
	valid := row(0x0100007f, 1234, 100)
	notListening := append([]byte{}, valid...)
	binary.LittleEndian.PutUint32(notListening, 5)
	reservedPortBits := append([]byte{}, valid...)
	reservedPortBits[10], reservedPortBits[11] = 0xff, 0xff
	for _, tc := range []struct {
		name  string
		rows  []byte
		count uint32
		want  int
	}{
		{"loopback", valid, 1, 100},
		{"wildcard", row(0, 1234, 100), 1, 100},
		{"not listening", notListening, 1, 0},
		{"port uses only first network-order 16 bits", reservedPortBits, 1, 100},
		{"other address", row(0x0200007f, 1234, 100), 1, 0},
		{"other port", row(0, 4321, 100), 1, 0},
		{"zero pid", row(0, 1234, 0), 1, 0},
		{"oversized pid", row(0, 1234, 0xffffffff), 1, 0},
		{"ambiguous", append(append([]byte{}, valid...), row(0, 1234, 101)...), 2, 0},
		{"truncated", valid[:23], 1, 0},
		{"bad count", valid, 0xffffffff, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := make([]byte, 4)
			binary.LittleEndian.PutUint32(table, tc.count)
			if got := listenerOwnerFromTable(append(table, tc.rows...), 1234); got != tc.want {
				t.Fatalf("owner = %d, want %d", got, tc.want)
			}
		})
	}
	if listenerOwnerFromTable(nil, 1234) != 0 {
		t.Fatal("missing table must be unavailable")
	}
}
