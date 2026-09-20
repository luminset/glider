package http

import (
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/nadoo/glider/pkg/log"
	"github.com/nadoo/glider/pkg/pool"
	"github.com/nadoo/glider/proxy"
)

// NewHTTPServer returns a http proxy server.
func NewHTTPServer(s string, p proxy.Proxy) (proxy.Server, error) {
	return NewHTTP(s, nil, p)
}

// ListenAndServe listens on server's addr and serves connections.
func (s *HTTP) ListenAndServe() {
	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Fatalf("[http] failed to listen on %s: %v", s.addr, err)
		return
	}
	defer l.Close()

	log.F("[http] listening TCP on %s", s.addr)

	for {
		c, err := l.Accept()
		if err != nil {
			log.F("[http] failed to accept: %v", err)
			continue
		}

		go s.Serve(c)
	}
}

// Serve serves a connection.
func (s *HTTP) Serve(cc net.Conn) {
	if c, ok := cc.(*net.TCPConn); ok {
		c.SetKeepAlive(true)
	}

	c := proxy.NewConn(cc)
	defer c.Close()

	req, err := parseRequest(c.Reader())
	if err != nil {
		log.F("[http] can not parse request from %s, error: %v", c.RemoteAddr(), err)
		texts := enTexts
		if errorPageEnabled() {
			sendErrorPage(c, "HTTP/1.1", "400 Bad Request", errorPage{
				status:  "400 Bad Request",
				version: Version,
				listen:  s.addr,
				title:   texts.titleBadRequest,
				desc:    texts.descBadRequest,
				method:  "",
				request: "",
				target:  "",
				client:  c.RemoteAddr().String(),
				reason:  err.Error(),
				hint:    texts.hintBadRequest,
				texts:   texts,
			})
		}
		return
	}

	if s.pretend {
		fmt.Fprintf(c, "%s 404 Not Found\r\nServer: nginx\r\n\r\n404 Not Found\r\n", req.proto)
		log.F("[http] %s <-> %s, pretend as web server", c.RemoteAddr().String(), s.Addr())
		return
	}

	s.servRequest(req, c)
}

func (s *HTTP) servRequest(req *request, c *proxy.Conn) {
	// Auth
	if s.user != "" && s.password != "" {
		if user, pass, ok := extractUserPass(req.auth); !ok || user != s.user || pass != s.password {
			io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\n\r\n")
			log.F("[http] auth failed from %s, auth info: %s:%s", c.RemoteAddr(), user, pass)
			return
		}
	}

	if req.method == "CONNECT" {
		s.servHTTPS(req, c)
		return
	}

	s.servHTTP(req, c)
}

func (s *HTTP) servHTTPS(r *request, c net.Conn) {
	rc, dialer, err := s.proxy.Dial("tcp", r.uri)
	if err != nil {
		if errorPageEnabled() {
			texts := textsFor(r.rawHeader.Get("Accept-Language"))
			if isReject(err) {
				sendErrorPage(c, r.proto, "502 Bad Gateway", errorPage{
					status:  "502 Bad Gateway",
					version: Version,
					listen:  s.addr,
					title:   texts.titleBlock,
					desc:    texts.descBlock,
					method:  r.method,
					request: r.uri,
					target:  r.uri,
					client:  c.RemoteAddr().String(),
					reason:  err.Error(),
					hint:    "",
					texts:   texts,
				})
			} else {
				sendErrorPage(c, r.proto, "502 Bad Gateway", errorPage{
					status:  "502 Bad Gateway",
					version: Version,
					listen:  s.addr,
					title:   texts.titleConnectFail,
					desc:    texts.descConnectFail,
					method:  r.method,
					request: r.uri,
					target:  r.uri,
					client:  c.RemoteAddr().String(),
					reason:  err.Error(),
					hint:    texts.hintConnectFail,
					texts:   texts,
				})
			}
		} else {
			sendPlainError(c, r.proto, "502 ERROR")
		}
		log.F("[https] %s <-> %s via %s, error in dial: %v", c.RemoteAddr(), r.uri, dialer.Addr(), err)
		return
	}
	defer rc.Close()

	io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")

	log.F("[https] %s <-> %s via %s", c.RemoteAddr(), r.uri, dialer.Addr())

	if err = proxy.Relay(c, rc); err != nil {
		log.F("[https] %s <-> %s via %s, relay error: %v", c.RemoteAddr(), r.uri, dialer.Addr(), err)
		// record remote conn failure only
		if !strings.Contains(err.Error(), s.addr) {
			s.proxy.Record(dialer, false)
		}
	}
}

func (s *HTTP) servHTTP(req *request, c *proxy.Conn) {
	rc, dialer, err := s.proxy.Dial("tcp", req.target)

	if headerLogEnabled("request") {
		log.F("[http] %s <-> %s via %s", c.RemoteAddr(), req.target, dialer.Addr())
		log.F("%s", formatHeaders("REQUEST header (client -> proxy)",
			c.RemoteAddr().String(),
			req.method+" "+req.uri+" "+req.proto, req.rawHeader))
	}

	if err != nil {
		texts := textsFor(req.rawHeader.Get("Accept-Language"))
		if errorPageEnabled() {
			if isReject(err) {
				sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
					status:  "502 Bad Gateway",
					version: Version,
					listen:  s.addr,
					title:   texts.titleBlock,
					desc:    texts.descBlock,
					method:  req.method,
					request: req.absuri,
					target:  req.target,
					client:  c.RemoteAddr().String(),
					reason:  err.Error(),
					hint:    "",
					texts:   texts,
				})
			} else {
				sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
					status:  "502 Bad Gateway",
					version: Version,
					listen:  s.addr,
					title:   texts.titleConnectFail,
					desc:    texts.descConnectFail,
					method:  req.method,
					request: req.absuri,
					target:  req.target,
					client:  c.RemoteAddr().String(),
					reason:  err.Error(),
					hint:    texts.hintConnectFail,
					texts:   texts,
				})
			}
		} else {
			sendPlainError(c, req.proto, "502 ERROR")
		}
		log.F("[http] %s <-> %s via %s, error in dial: %v", c.RemoteAddr(), req.target, dialer.Addr(), err)
		return
	}
	defer rc.Close()

	buf := pool.GetBytesBuffer()
	defer pool.PutBytesBuffer(buf)

	// send request to remote server
	req.WriteBuf(buf)
	_, err = rc.Write(buf.Bytes())
	if err != nil {
		if errorPageEnabled() {
			texts := textsFor(req.rawHeader.Get("Accept-Language"))
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:  "502 Bad Gateway",
				version: Version,
				listen:  s.addr,
				title:   texts.titleWriteFail,
				desc:    texts.descWriteFail,
				method:  req.method,
				request: req.absuri,
				target:  req.target,
				client:  c.RemoteAddr().String(),
				reason:  err.Error(),
				hint:    "",
				texts:   texts,
			})
		}
		return
	}

	// copy the left request bytes to remote server. eg. length specificed or chunked body.
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
		if errorPageEnabled() {
			texts := textsFor(req.rawHeader.Get("Accept-Language"))
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:  "502 Bad Gateway",
				version: Version,
				listen:  s.addr,
				title:   texts.titleUpstreamEOF,
				desc:    texts.descUpstreamEOF,
				method:  req.method,
				request: req.absuri,
				target:  req.target,
				client:  c.RemoteAddr().String(),
				reason:  err.Error(),
				hint:    "",
				texts:   texts,
			})
		}
		return
	}

	proto, code, status, ok := parseStartLine(line)
	if !ok {
		if errorPageEnabled() {
			texts := textsFor(req.rawHeader.Get("Accept-Language"))
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:  "502 Bad Gateway",
				version: Version,
				listen:  s.addr,
				title:   texts.titleBadStart,
				desc:    texts.descBadStart,
				method:  req.method,
				request: req.absuri,
				target:  req.target,
				client:  c.RemoteAddr().String(),
				reason:  line,
				hint:    "",
				texts:   texts,
			})
		}
		return
	}

	header, err := tpr.ReadMIMEHeader()
	if err != nil {
		log.F("[http] read header error:%s", err)
		if errorPageEnabled() {
			texts := textsFor(req.rawHeader.Get("Accept-Language"))
			sendErrorPage(c, req.proto, "502 Bad Gateway", errorPage{
				status:  "502 Bad Gateway",
				version: Version,
				listen:  s.addr,
				title:   texts.titleBadStart,
				desc:    texts.descBadHeader,
				method:  req.method,
				request: req.absuri,
				target:  req.target,
				client:  c.RemoteAddr().String(),
				reason:  err.Error(),
				hint:    "",
				texts:   texts,
			})
		}
		return
	}

	if headerLogEnabled("response") {
		log.F("[http] %s <-> %s via %s", c.RemoteAddr(), req.target, dialer.Addr())
		log.F("%s", formatHeaders("RESPONSE header (server -> proxy)",
			c.RemoteAddr().String(),
			proto+" "+code+" "+status, header))
	}

	header.Set("Proxy-Connection", "close")
	header.Set("Connection", "close")

	buf.Reset()
	writeStartLine(buf, proto, code, status)
	writeHeaders(buf, header)

	log.F("[http] %s <-> %s via %s", c.RemoteAddr(), req.target, dialer.Addr())
	c.Write(buf.Bytes())

	proxy.Copy(c, r)
}
