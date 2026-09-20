//go:build windows

package main

import (
	"syscall"
)

// init 在 Windows 下设置控制台代码页为 UTF-8 (65001)
// 解决 Go 程序 UTF-8 输出被 Windows 控制台按 GBK 解码导致的中文乱码
func init() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setConsoleOutputCP := kernel32.NewProc("SetConsoleOutputCP")
	setConsoleCP := kernel32.NewProc("SetConsoleCP")
	// 设置输出和输入代码页为 UTF-8
	setConsoleOutputCP.Call(uintptr(65001))
	setConsoleCP.Call(uintptr(65001))
}
