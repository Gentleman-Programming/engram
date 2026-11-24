//go:build !linux

package main

import (
	"context"
	"errors"
	"net"
)

func inheritedOwnedListener() (net.Listener, error) { return nil, nil }

func retainOwnedStoreLease(context.Context, string) error {
	return errors.New("session-owned store leases require Linux")
}
