//go:build !unix

package server

import (
	"fmt"
	"net"
	"os"
)

func listenSecureUnixSocket(string) (net.Listener, os.FileInfo, error) {
	return nil, nil, fmt.Errorf("Unix sockets are not supported on this platform")
}
