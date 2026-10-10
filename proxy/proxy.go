package proxy

import (
	"net"
	"strings"
)

// Proxy is a dialer manager.
type Proxy interface {
	// Dial connects to the given address via the proxy.
	Dial(network, addr string) (c net.Conn, dialer Dialer, err error)

	// DialUDP connects to the given address via the proxy.
	DialUDP(network, addr string) (pc net.PacketConn, dialer UDPDialer, err error)

	// Get the dialer by dstAddr.
	NextDialer(dstAddr string) Dialer

	// Record records result while using the dialer from proxy.
	Record(dialer Dialer, success bool)
}

// Hostname returns the host part of a "host:port" address, without the
// brackets that wrap an IPv6 literal, so the result can be used as a TLS
// server name or a Host header value. addr is returned unchanged when it
// carries no port.
func Hostname(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// SetDefaultPort returns addr with defaultPort appended when addr carries no
// port.
//
// It also normalizes an IPv6 literal so that it ends up bracketed exactly once.
// The obvious "if SplitHostPort fails, JoinHostPort it" idiom silently breaks
// the bracketed-but-portless form: JoinHostPort adds brackets on its own, so
// "[::1]" would become the unusable "[[::1]]:443". The unbracketed "::1" keeps
// working, which makes the failure look like an IPv6 problem instead of a
// missing port.
func SetDefaultPort(addr, defaultPort string) string {
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if port == "" {
			port = defaultPort
		}
		return net.JoinHostPort(host, port)
	}

	// SplitHostPort failed: either there is no port at all, or addr is a bare
	// or bracketed IPv6 literal (it has colons, but none introduces a port).
	host := strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
	return net.JoinHostPort(host, defaultPort)
}

var (
	msg    strings.Builder
	usages = make(map[string]string)
)

// AddUsage adds help message for the named proxy.
func AddUsage(name, usage string) {
	usages[name] = usage
	msg.WriteString(usage)
	msg.WriteString("\n--")
}

// Usage returns help message of the named proxy.
func Usage(name string) string {
	if name == "all" {
		return msg.String()
	}

	if usage, ok := usages[name]; ok {
		return usage
	}

	return "can not find usage for: " + name
}
