package rule

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/nadoo/glider/pkg/pool"
	"github.com/nadoo/glider/proxy"
)

// Checker is a forwarder health checker.
type Checker interface {
	Check(fwdr *Forwarder) (elap time.Duration, err error)
}

type tcpChecker struct {
	addr    string
	timeout time.Duration
}

func newTcpChecker(addr string, timeout time.Duration) *tcpChecker {
	return &tcpChecker{proxy.SetDefaultPort(addr, "80"), timeout}
}

// Check implements the Checker interface.
func (c *tcpChecker) Check(fwdr *Forwarder) (time.Duration, error) {
	startTime := time.Now()
	rc, err := fwdr.Dial("tcp", c.addr)
	if err != nil {
		return 0, err
	}
	rc.Close()
	return time.Since(startTime), nil
}

type httpChecker struct {
	addr    string
	uri     string
	expect  string
	timeout time.Duration

	tlsConfig  *tls.Config
	serverName string

	regex *regexp.Regexp
}

func newHttpChecker(addr, uri, expect string, timeout time.Duration, withTLS bool) *httpChecker {
	c := &httpChecker{
		addr:    addr,
		uri:     uri,
		expect:  expect,
		timeout: timeout,
		regex:   regexp.MustCompile(expect),
	}

	if withTLS {
		c.addr = proxy.SetDefaultPort(addr, "443")
	} else {
		c.addr = proxy.SetDefaultPort(addr, "80")
	}
	// serverName keeps the bracketed IPv6 form for the Host header, while the
	// TLS server name must be bracket-free to allow IP SAN verification.
	c.serverName = c.addr[:strings.LastIndex(c.addr, ":")]
	if withTLS {
		c.tlsConfig = &tls.Config{ServerName: proxy.Hostname(c.addr)}
	}
	return c
}

// Check implements the Checker interface.
func (c *httpChecker) Check(fwdr *Forwarder) (time.Duration, error) {
	startTime := time.Now()
	rc, err := fwdr.Dial("tcp", c.addr)
	if err != nil {
		return 0, err
	}

	if c.tlsConfig != nil {
		tlsConn := tls.Client(rc, c.tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			tlsConn.Close()
			return 0, err
		}
		rc = tlsConn
	}
	defer rc.Close()

	if c.timeout > 0 {
		rc.SetDeadline(time.Now().Add(c.timeout))
	}

	if _, err = io.WriteString(rc,
		"GET "+c.uri+" HTTP/1.1\r\nHost:"+c.serverName+"\r\n\r\n"); err != nil {
		return 0, err
	}

	r := pool.GetBufReader(rc)
	defer pool.PutBufReader(r)

	line, err := r.ReadString('\n')
	if err != nil {
		return 0, err
	}

	if !c.regex.MatchString(line) {
		return 0, fmt.Errorf("expect: %s, got: %s", c.expect, line)
	}

	elapsed := time.Since(startTime)
	if elapsed > c.timeout {
		return elapsed, errors.New("timeout")
	}

	return elapsed, nil
}

type fileChecker struct{ path string }

func newFileChecker(path string) *fileChecker { return &fileChecker{path} }

// Check implements the Checker interface.
func (c *fileChecker) Check(fwdr *Forwarder) (time.Duration, error) {
	cmd := exec.Command(c.path)
	cmd.Stdout = os.Stdout
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "FORWARDER_ADDR="+fwdr.Addr())
	cmd.Env = append(cmd.Env, "FORWARDER_URL="+fwdr.URL())
	return 0, cmd.Run()
}
