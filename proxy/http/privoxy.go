package http

// Privoxy custom-domain compatibility (opt-in, disabled by default).
//
// privoxy can be configured to answer user-defined domains that do not exist
// in DNS: a host pattern such as `da` (matching `*.da`) plus a `+redirect{}`
// or `+block{}` action makes privoxy return its own page instead of trying to
// resolve the name. Two privoxy properties make such domains impossible to
// reach through a plain CONNECT-based upstream chain:
//
//   - privoxy only applies its URL actions to requests it proxies itself.
//     A CONNECT request is a raw tunnel and is relayed untouched, so the
//     `.da` action never fires.
//   - privoxy's default `limit-connect{443}` even refuses CONNECT to port 80.
//
// glider's http forwarder always speaks CONNECT to its upstream, so a request
// for such a domain produces an empty/refused tunnel (EOF) or privoxy's own
// "could not resolve" page (404), and the redirect page never shows up.
//
// Two ways to route around that, both gated by the same master switch
// `-privoxycompat=on` (off by default, so a stale configuration can never
// change behaviour silently):
//
//  1. Rules (recommended, per-rule routing):
//       forward=privoxy://127.0.0.1:8118
//       domain=da
//     The `privoxy://` dialer (see below) connects to privoxy and marks the
//     connection as needing proxy-style requests; the http server then writes
//     the request with an absolute URI instead of tunnelling it.
//
//  2. Global passthrough (no rule file needed):
//       privoxyaddr=127.0.0.1:8118
//       privoxydomain=da
//     Requests whose host matches one of those suffixes bypass the rule chain
//     entirely and go straight to privoxy, the same way.
//
// Either way privoxy's answer -- including its 302 redirect -- is relayed back
// to the client unchanged. This covers plain HTTP only: privoxy cannot apply
// URL actions to a TLS tunnel, so a CONNECT to a privoxy domain is refused
// with a clear error instead of silently producing a broken tunnel.

import (
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/nadoo/glider/pkg/log"
	"github.com/nadoo/glider/pkg/pool"
	"github.com/nadoo/glider/proxy"
)

var (
	// privoxyEnabled is the master switch (-privoxycompat=on).
	privoxyEnabled bool
	// privoxyPassthrough is the global passthrough mode, which additionally
	// needs privoxyaddr + privoxydomain to be configured.
	privoxyPassthrough bool
	privoxyAddr        string
	privoxyDomains     []string
	privoxyTimeout     = 10 * time.Second
)

// SetPrivoxyCompat configures the privoxy custom-domain compatibility. Any
// mode other than "on" leaves everything switched off (the default), so
// existing behaviour is never changed unless it is asked for. addr accepts
// "host:port" with an optional scheme prefix; domains are matched as host
// suffixes (a leading dot is optional, so both "da" and ".da" work, matching
// "da" and "*.da"). addr and domains are only needed for the global
// passthrough mode -- rule-file routing via `forward=privoxy://host:port`
// needs nothing but the master switch.
func SetPrivoxyCompat(mode, addr string, domains []string, timeout time.Duration) {
	privoxyEnabled, privoxyPassthrough = false, false
	privoxyAddr, privoxyDomains = "", nil

	if strings.ToLower(strings.TrimSpace(mode)) != "on" {
		return
	}

	privoxyEnabled = true
	if timeout > 0 {
		privoxyTimeout = timeout
	}

	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "tcp://")

	var list []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(d, ".")
		if d != "" {
			list = append(list, d)
		}
	}

	log.F("[privoxy] compat enabled: rule mode available via forward=privoxy://HOST:PORT")

	if addr != "" && len(list) > 0 {
		privoxyAddr, privoxyDomains, privoxyPassthrough = addr, list, true
		log.F("[privoxy] global passthrough enabled: addr=%s, domains=%s, timeout=%s",
			privoxyAddr, strings.Join(privoxyDomains, ","), privoxyTimeout)
	}
}

// privoxyCompatEnabled reports whether the master switch is on.
func privoxyCompatEnabled() bool {
	return privoxyEnabled
}

// privoxyPassthroughEnabled reports whether the global passthrough mode is on.
func privoxyPassthroughEnabled() bool {
	return privoxyPassthrough
}

// isPrivoxyDomain reports whether dstAddr (host[:port]) belongs to one of the
// globally configured privoxy domain suffixes.
func isPrivoxyDomain(dstAddr string) bool {
	if !privoxyPassthrough {
		return false
	}

	host := dstAddr
	if h, _, err := net.SplitHostPort(dstAddr); err == nil {
		host = h
	}

	// normalize: lowercase, drop [] around literals, drop the FQDN root dot.
	host = strings.ToLower(strings.Trim(host, "[]"))
	host = strings.TrimSuffix(host, ".")

	for _, d := range privoxyDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}

	return false
}

// absoluteURIDialer is implemented by dialers that must receive proxy-style
// requests, where the request line carries the absolute URI. Origin-form
// upstreams (direct, socks5, ...) keep the relative form.
type absoluteURIDialer interface {
	AbsoluteURI() bool
}

// usesAbsoluteURI reports whether the dialer asks for absolute-URI requests.
func usesAbsoluteURI(d proxy.Dialer) bool {
	if a, ok := d.(absoluteURIDialer); ok {
		return a.AbsoluteURI()
	}
	return false
}

// ---------------------------------------------------------------------------
// privoxy:// dialer (rule-file routing)
// ---------------------------------------------------------------------------

// PrivoxyDialer reaches a privoxy instance and tells the http server to speak
// to it in proxy form (absolute URI) instead of opening a CONNECT tunnel.
type PrivoxyDialer struct {
	addr   string
	dialer proxy.Dialer
}

// NewPrivoxyDialer returns a privoxy dialer from a URL like
// `privoxy://HOST:PORT`. d is the base dialer used to reach privoxy.
func NewPrivoxyDialer(s string, d proxy.Dialer) (proxy.Dialer, error) {
	if d == nil {
		return nil, errors.New("[privoxy] privoxy dialer needs a base dialer")
	}

	if !strings.Contains(s, "://") {
		s += "://"
	}

	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("[privoxy] parse %q error: %v", s, err)
	}

	if u.Host == "" {
		return nil, errors.New("[privoxy] privoxy address is empty, format: privoxy://HOST:PORT")
	}

	return &PrivoxyDialer{addr: u.Host, dialer: d}, nil
}

// Addr returns the privoxy address.
func (d *PrivoxyDialer) Addr() string { return d.addr }

// Dial connects to privoxy. No CONNECT is ever sent: the request is forwarded
// in proxy form by the http server, which is the only form privoxy applies its
// own actions to. CONNECT-only destinations (https) are rejected explicitly so
// the failure is visible instead of producing a broken TLS tunnel.
func (d *PrivoxyDialer) Dial(network, addr string) (net.Conn, error) {
	if !privoxyEnabled {
		return nil, errors.New("[privoxy] privoxy compat is off, enable it with -privoxycompat=on")
	}

	if _, port, err := net.SplitHostPort(addr); err == nil && port == "443" {
		return nil, errors.New("[privoxy] privoxy:// relays plain http requests only (absolute URI), " +
			"use http:// for CONNECT-based https: " + addr)
	}

	return d.dialer.Dial(network, d.addr)
}

// AbsoluteURI marks the connections of this dialer as needing proxy-style
// (absolute-URI) requests.
func (d *PrivoxyDialer) AbsoluteURI() bool { return true }

// DialUDP is not supported.
func (d *PrivoxyDialer) DialUDP(network, addr string) (net.PacketConn, error) {
	return nil, proxy.ErrNotSupported
}

func init() {
	proxy.RegisterDialer("privoxy", NewPrivoxyDialer)
}

func init() {
	proxy.AddUsage("privoxy", `
Privoxy scheme (custom-domain compatibility, requires -privoxycompat=on):
  privoxy://HOST:PORT

Sends requests to a privoxy instance in proxy form (absolute URI, no CONNECT),
so privoxy actions such as +redirect{} on user-defined domains take effect.
Plain http only; use http:// for CONNECT-based https.

  glider -listen http://:8080 -privoxycompat=on -rules-dir rules

  # rules/privoxy.rule
  #   forward=privoxy://127.0.0.1:8118
  #   domain=da
  #   check=disable
`)
}

// ---------------------------------------------------------------------------
// Global passthrough (privoxyaddr + privoxydomain)
// ---------------------------------------------------------------------------

// servPrivoxy forwards one request to privoxy in proxy form (absolute URI).
// Sending the absolute URI is what makes privoxy treat the request as a real
// proxy request, so its own actions apply; the response is relayed as-is so a
// 302 from privoxy reaches the browser intact.
func (s *HTTP) servPrivoxy(req *request, c *proxy.Conn) {
	texts := textsFor(req.rawHeader.Get("Accept-Language"))
	dst := privoxyAddr

	rc, err := net.DialTimeout("tcp", dst, privoxyTimeout)
	if err != nil {
		if errorPageEnabled() {
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:    "502 Bad Gateway",
				version:   Version,
				listen:    s.addr,
				title:     texts.titleConnectFail,
				desc:      texts.descConnectFail,
				request:   req.absuri,
				target:    req.target,
				client:    c.RemoteAddr().String(),
				via:       dst,
				stage:     texts.stageDial,
				reason:    err.Error(),
				condition: texts.condConnectFail,
				severity:  severityError,
				hint:      texts.hintConnectFail,
				texts:     texts,
			})
		} else {
			sendPlainError(c, req.proto, "502 ERROR")
		}
		log.F("[privoxy] %s <-> %s, dial to privoxy %s failed: %v", c.RemoteAddr(), req.target, dst, err)
		return
	}
	defer rc.Close()

	buf := pool.GetBytesBuffer()
	defer pool.PutBytesBuffer(buf)

	req.WriteAbsBuf(buf)
	if _, err = rc.Write(buf.Bytes()); err != nil {
		if errorPageEnabled() {
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:    "502 Bad Gateway",
				version:   Version,
				listen:    s.addr,
				title:     texts.titleWriteFail,
				desc:      texts.descWriteFail,
				request:   req.absuri,
				target:    req.target,
				client:    c.RemoteAddr().String(),
				via:       dst,
				stage:     texts.stageWrite,
				reason:    err.Error(),
				condition: texts.condInternalError,
				severity:  severityError,
				hint:      "",
				texts:     texts,
			})
		}
		log.F("[privoxy] %s <-> %s, write to privoxy %s failed: %v", c.RemoteAddr(), req.target, dst, err)
		return
	}

	// forward the remaining client bytes (e.g. a request body) to privoxy.
	go func() {
		if _, err := c.Reader().Peek(1); err == nil {
			proxy.Copy(rc, c)
			rc.SetDeadline(time.Now())
			c.SetDeadline(time.Now())
		}
	}()

	r := pool.GetBufReader(rc)
	defer pool.PutBufReader(r)

	tpr := textproto.NewReader(r)
	line, err := tpr.ReadLine()
	if err != nil {
		// privoxy closed the connection without answering: report it instead
		// of leaving the browser with an empty page.
		if errorPageEnabled() {
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:    "502 Bad Gateway",
				version:   Version,
				listen:    s.addr,
				title:     texts.titleUpstreamEOF,
				desc:      texts.descUpstreamEOF,
				request:   req.absuri,
				target:    req.target,
				client:    c.RemoteAddr().String(),
				via:       dst,
				stage:     texts.stageRead,
				reason:    err.Error(),
				condition: texts.condUpstreamError,
				severity:  severityWarning,
				hint:      "",
				texts:     texts,
			})
		}
		log.F("[privoxy] %s <-> %s via %s, no response: %v", c.RemoteAddr(), req.target, dst, err)
		return
	}

	proto, code, status, ok := parseStartLine(line)
	if !ok {
		if errorPageEnabled() {
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:    "502 Bad Gateway",
				version:   Version,
				listen:    s.addr,
				title:     texts.titleBadStart,
				desc:      texts.descBadStart,
				request:   req.absuri,
				target:    req.target,
				client:    c.RemoteAddr().String(),
				via:       dst,
				stage:     texts.stageRead,
				reason:    line,
				condition: texts.condUpstreamError,
				severity:  severityWarning,
				hint:      "",
				texts:     texts,
			})
		}
		return
	}

	header, err := tpr.ReadMIMEHeader()
	if err != nil {
		if errorPageEnabled() {
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:    "502 Bad Gateway",
				version:   Version,
				listen:    s.addr,
				title:     texts.titleBadStart,
				desc:      texts.descBadHeader,
				request:   req.absuri,
				target:    req.target,
				client:    c.RemoteAddr().String(),
				via:       dst,
				stage:     texts.stageReadHeader,
				reason:    err.Error(),
				condition: texts.condUpstreamError,
				severity:  severityWarning,
				hint:      "",
				texts:     texts,
			})
		}
		return
	}

	header.Set("Proxy-Connection", "close")
	header.Set("Connection", "close")

	buf.Reset()
	writeStartLine(buf, proto, code, status)
	writeHeaders(buf, header)

	log.F("[privoxy] %s <-> %s via %s: %s %s %s", c.RemoteAddr(), req.target, dst, proto, code, status)
	c.Write(buf.Bytes())

	proxy.Copy(c, r)
}
