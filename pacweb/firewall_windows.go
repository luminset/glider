//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ensureFirewallRule 确保本程序在 Windows 防火墙中有入站允许规则
// 如果规则不存在则自动添加，添加成功返回 true
func ensureFirewallRule() bool {
	exePath, err := os.Executable()
	if err != nil {
		logf("[防火墙] 无法获取程序路径: %v", err)
		return false
	}
	// 转为短路径避免空格问题
	exePath, _ = filepath.Abs(exePath)

	ruleName := "pacweb HTTP Service"

	// 1. 检查规则是否已存在
	if hasFirewallRule(ruleName, exePath) {
		logf("[防火墙] 规则已存在: %s", ruleName)
		return true
	}

	// 2. 添加规则（程序级入站允许）
	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+ruleName,
		"dir=in",
		"action=allow",
		"program="+exePath,
		"enable=yes",
		"profile=any",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logf("[防火墙] 添加规则失败: %v", err)
		logf("[防火墙] 输出: %s", string(output))
		if strings.Contains(string(output), "需要管理员权限") || strings.Contains(string(output), "administrator") {
			logf("[防火墙] 请以管理员身份运行本程序，或手动执行以下命令添加规则:")
			logf("  netsh advfirewall firewall add rule name=\"%s\" dir=in action=allow program=\"%s\" enable=yes", ruleName, exePath)
		}
		return false
	}

	logf("[防火墙] 已添加入站允许规则: %s (program=%s)", ruleName, exePath)
	return true
}

// hasFirewallRule 检查指定名称的防火墙规则是否已存在且指向本程序
// 返回 true 表示规则存在且路径匹配；返回 false 表示需要（重新）添加
// 如果旧规则路径与当前程序路径不一致（如程序被移动），会先删除旧规则
func hasFirewallRule(ruleName, exePath string) bool {
	cmd := exec.Command("netsh", "advfirewall", "firewall", "show", "rule",
		"name="+ruleName)
	output, err := cmd.Output()
	if err != nil {
		// 规则不存在时 netsh 返回非零
		return false
	}

	out := string(output)
	if !strings.Contains(out, ruleName) {
		return false
	}

	// 提取规则中的 Program 路径
	oldPath := extractProgramPath(out)
	current := normalizePath(exePath)

	if oldPath == "" {
		// 规则存在但未找到 Program 字段（可能是端口级规则），删除后重建
		deleteFirewallRule(ruleName)
		logf("[防火墙] 旧规则缺少 Program 字段，已删除并将重建")
		return false
	}

	if normalizePath(oldPath) == current {
		return true
	}

	// 路径不一致（程序被移动过），删除旧规则
	deleteFirewallRule(ruleName)
	logf("[防火墙] 检测到程序路径变更: %s → %s，已删除旧规则", oldPath, exePath)
	return false
}

// extractProgramPath 从 netsh show rule 输出中提取 Program 字段值
func extractProgramPath(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Program:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Program:"))
		}
	}
	return ""
}

// normalizePath 归一化路径用于比较（转小写、去除引号、统一分隔符）
func normalizePath(p string) string {
	p = strings.Trim(p, "\"")
	p = strings.ToLower(p)
	p = strings.ReplaceAll(p, "/", "\\")
	return p
}

// deleteFirewallRule 删除指定名称的防火墙规则
func deleteFirewallRule(ruleName string) {
	delCmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+ruleName)
	delCmd.Run()
}

// logf 简单的日志输出包装
func logf(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}
