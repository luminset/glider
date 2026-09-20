//go:build !windows

package main

import "strings"

// findPortOwner 非 Windows 平台的存根实现
func findPortOwner(addr string) string {
	return ""
}

// isAddrInUseError 判断错误是否为地址被占用
func isAddrInUseError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "bind:")
}
