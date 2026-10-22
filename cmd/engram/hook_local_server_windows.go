//go:build windows

package main

import "golang.org/x/sys/windows"

const hookConnectionRefused = windows.WSAECONNREFUSED
