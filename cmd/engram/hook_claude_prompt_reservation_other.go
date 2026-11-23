//go:build !windows

package main

func reserveClaudeWindowsPrompt(string) (func(), bool) {
	return nil, false
}
