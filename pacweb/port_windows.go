//go:build windows

package main

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// findPortOwner 查询占用指定端口的进程信息（Windows 实现）
// 返回格式: "PID=1234, 进程名=xxx.exe"，若无法查询则返回空字符串
func findPortOwner(addr string) string {
	// 从监听地址中提取端口号
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// 可能只有端口号（如 ":8088" 或 "8088"）
		portStr = strings.TrimPrefix(addr, ":")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return ""
	}

	// 方式1：用 netstat 查找占用端口的 PID
	pid := findPIDByNetstat(port)
	if pid == 0 {
		// 方式2：用 PowerShell 查找
		pid = findPIDByPowerShell(port)
	}

	if pid == 0 {
		return ""
	}

	// 获取进程名
	name := findProcessNameByTasklist(pid)
	if name == "" {
		name = findProcessNameByPowerShell(pid)
	}

	if name != "" {
		return fmt.Sprintf("PID=%d, 进程名=%s", pid, name)
	}
	return fmt.Sprintf("PID=%d", pid)
}

// findPIDByNetstat 通过 netstat -ano 查找监听指定端口的 PID
func findPIDByNetstat(port int) int {
	cmd := exec.Command("netstat", "-ano", "-p", "TCP")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TCP") {
			continue
		}
		fields := strings.Fields(line)
		// 格式: Proto  Local Address  Foreign Address  State  PID
		// 例如: TCP    0.0.0.0:8088    0.0.0.0:0    LISTENING    1234
		if len(fields) < 5 {
			continue
		}
		state := fields[3]
		if state != "LISTENING" {
			continue
		}
		// 提取本地地址中的端口
		localAddr := fields[1]
		_, p, err := net.SplitHostPort(localAddr)
		if err != nil {
			continue
		}
		if p == strconv.Itoa(port) {
			pid, err := strconv.Atoi(fields[4])
			if err == nil {
				return pid
			}
		}
	}
	return 0
}

// findPIDByPowerShell 通过 PowerShell Get-NetTCPConnection 查找 PID
func findPIDByPowerShell(port int) int {
	script := fmt.Sprintf(`(Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue).OwningProcess`, port)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0
	}
	return pid
}

// findProcessNameByTasklist 通过 tasklist 查找进程名
func findProcessNameByTasklist(pid int) string {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// CSV 格式: "进程名","PID","会话名","会话#","内存使用"
	line := strings.TrimSpace(string(output))
	if line == "" || strings.HasPrefix(line, "INFO:") {
		return ""
	}
	// 去掉引号，取第一个字段
	line = strings.Trim(line, "\"")
	name := strings.SplitN(line, "\"", 2)[0]
	return name
}

// findProcessNameByPowerShell 通过 PowerShell Get-Process 查找进程名
func findProcessNameByPowerShell(pid int) string {
	script := fmt.Sprintf(`(Get-Process -Id %d -ErrorAction SilentlyContinue).ProcessName`, pid)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(output))
	if name == "" {
		return ""
	}
	return name + ".exe"
}

// isAddrInUseError 判断错误是否为地址被占用
func isAddrInUseError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Only one usage of each socket address") ||
		strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "bind:")
}
