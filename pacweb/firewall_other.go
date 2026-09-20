//go:build !windows

package main

// ensureFirewallRule 非 Windows 平台存根
func ensureFirewallRule() bool {
	return true
}
