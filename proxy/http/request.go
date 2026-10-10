package http

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/nadoo/glider/pkg/log"
)

// Methods are http methods from rfc.
// https://www.rfc-editor.org/rfc/rfc2616, http methods must be uppercase
var Methods = [...][]byte{
	[]byte("GET"),
	[]byte("POST"),
	[]byte("PUT"),
	[]byte("DELETE"),
	[]byte("CONNECT"),
	[]byte("HEAD"),
	[]byte("OPTIONS"),
	[]byte("TRACE"),
	[]byte("PATCH"),
}

type request struct {
	method string
	uri    string
	proto  string
	auth   string
	header textproto.MIMEHeader

	rawHeader textproto.MIMEHeader // original headers as received from the client, before cleaning

	target string // target host with port
	ruri   string // relative uri
	absuri string // absolute uri
}

func parseRequest(r *bufio.Reader) (*request, error) {
	tpr := textproto.NewReader(r)
	line, err := tpr.ReadLine()
	if err != nil {
		return nil, err
	}

	method, uri, proto, ok := parseStartLine(line)
	if !ok {
		return nil, fmt.Errorf("error in parseStartLine: %s", line)
	}

	header, err := tpr.ReadMIMEHeader()
	if err != nil {
		log.F("[http] read header error:%s", err)
		return nil, err
	}

	auth := header.Get("Proxy-Authorization")

	// keep a deep copy of the original headers as received, for header logging
	rawHeader := make(textproto.MIMEHeader, len(header))
	for k, vv := range header {
		rawHeader[k] = append([]string(nil), vv...)
	}

	cleanHeaders(header)
	header.Set("Connection", "close")

	// https://github.com/golang/go/blob/dcf0929de6a12103a8fd7097abd6e797188c366d/src/net/http/request.go#L1047
	justAuthority := method == "CONNECT" && !strings.HasPrefix(uri, "/")
	if justAuthority {
		uri = "http://" + uri
	}

	u, err := url.ParseRequestURI(uri)
	if err != nil {
		// RFC 3986, section 3.2.2 requires an IPv6 literal in a URI to be
		// bracketed, but some clients send it bare. Such a literal is accepted
		// here only when it has exactly one legal reading: with two readings the
		// address/port split would be a guess, and guessing wrong would send the
		// request to another host. Ambiguous literals stay rejected.
		if bracketed, ok := bracketBareIPv6URI(uri); ok {
			if fixed, ferr := url.ParseRequestURI(bracketed); ferr == nil {
				log.F("[http] bare IPv6 literal in request URI: %s -> %s", uri, bracketed)
				u, err, uri = fixed, nil, bracketed
			}
		}
	}

	if err != nil {
		log.F("[http] parse request url error: %s, uri: %s", err, uri)
		return nil, err
	}

	if justAuthority {
		// Strip the bogus "http://" back off.
		u.Scheme = ""
		uri = uri[7:]
	}

	// a bracketed IPv6 literal already contains ':' without carrying a port, so
	// the port must be taken from the parsed URL instead of looking for ':'.
	tgt := u.Host
	if u.Port() == "" {
		tgt = net.JoinHostPort(u.Hostname(), "80")
	}

	req := &request{
		method:    method,
		uri:       uri,
		proto:     proto,
		auth:      auth,
		header:    header,
		rawHeader: rawHeader,
		target:    tgt,
	}

	if u.IsAbs() {
		req.absuri = u.String()
		u.Scheme, u.Host = "", ""
		req.ruri = u.String()
	} else {
		req.ruri = u.String()
		base, err := url.Parse("http://" + header.Get("Host"))
		if err != nil {
			return nil, err
		}
		u = base.ResolveReference(u)
		req.absuri = u.String()
	}

	return req, nil
}

// bracketBareIPv6URI returns uri with an unbracketed IPv6 literal in its
// authority wrapped in brackets, and reports whether it changed the uri. Only a
// literal with exactly one legal reading is rewritten, so the split between
// address and port is never a guess; anything else is returned unchanged and
// keeps failing the caller's url.ParseRequestURI.
func bracketBareIPv6URI(uri string) (string, bool) {
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri, false
	}
	scheme, rest := uri[:i+3], uri[i+3:]

	authority, tail := rest, ""
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		authority, tail = rest[:j], rest[j:]
	}

	// Already bracketed, or not a bare IPv6 literal: a host carrying a port has
	// at most one colon, and a userinfo form ("user:pass@host") is left alone so
	// that only a plain authority is ever rewritten.
	if strings.ContainsAny(authority, "[]") || strings.Count(authority, ":") < 2 {
		return uri, false
	}

	addr, port, ok := onlyIPv6Reading(authority)
	if !ok {
		return uri, false
	}

	if port == "" {
		return scheme + "[" + addr + "]" + tail, true
	}
	return scheme + "[" + addr + "]:" + port + tail, true
}

// onlyIPv6Reading returns the single legal reading of a bare
// "IPv6-literal[:port]" string as an address and an optional port. It reports
// ok == false when the string has no reading at all, or more than one.
//
// Two readings are common because "::" may stand for one or more groups of
// zeros: "2001:db8::5e:443" reads both as the address "2001:db8::5e" with port
// 443 and as the eight-group address "2001:db8::5e:443" with no port. The
// second reading exists only when the trailing group is a valid IPv6 group,
// which holds four hex digits at most: a five-digit tail such as "14000"
// cannot be a group, so the port reading is the only one left.
func onlyIPv6Reading(s string) (addr, port string, ok bool) {
	readings := 0

	if isIPv6Literal(s) {
		addr, port, readings = s, "", 1
	}

	for i := len(s) - 1; i > 0; i-- {
		if s[i] != ':' {
			continue
		}
		head, p := s[:i], s[i+1:]
		if !isPortNumber(p) || !isIPv6Literal(head) {
			continue
		}
		addr, port, readings = head, p, readings+1
	}

	if readings != 1 {
		return "", "", false
	}
	return addr, port, true
}

// isIPv6Literal reports whether s is an unbracketed IPv6 address.
func isIPv6Literal(s string) bool {
	ip, err := netip.ParseAddr(s)
	return err == nil && ip.Is6()
}

// isPortNumber reports whether s is a decimal TCP/UDP port number.
func isPortNumber(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}

func (r *request) WriteBuf(buf *bytes.Buffer) {
	writeStartLine(buf, r.method, r.ruri, r.proto)
	writeHeaders(buf, r.header)
}

func (r *request) WriteAbsBuf(buf *bytes.Buffer) {
	writeStartLine(buf, r.method, r.absuri, r.proto)
	writeHeaders(buf, r.header)
}
