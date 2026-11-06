//go:build !windows

package main

import "syscall"

const hookConnectionRefused = syscall.ECONNREFUSED
