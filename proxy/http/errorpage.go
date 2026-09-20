package http

import (
	"fmt"
	"html"
	"io"
	"net"
	"strings"
	"time"
)

// Version is the glider version shown on error pages. It is set once at
// startup by the main package (which owns the canonical version variable).
var Version = "dev"

// errorPageMode controls whether glider sends a styled HTML error page on
// http proxy errors. Valid values: "on", "off". Default is "off", which
// keeps the original plain `502 ERROR` response behaviour.
var errorPageMode = "off"

// SetErrorPageMode sets whether styled HTML error pages are sent.
// Any value other than "on" falls back to "off".
func SetErrorPageMode(mode string) {
	if strings.ToLower(strings.TrimSpace(mode)) == "on" {
		errorPageMode = "on"
	} else {
		errorPageMode = "off"
	}
}

// errorPageEnabled reports whether styled HTML error pages are enabled.
func errorPageEnabled() bool {
	return errorPageMode == "on"
}

// isReject reports whether the dial error came from a reject:// forwarder
// (a proxy rule pointing to reject://). Reject dialers always fail with the
// literal error "REJECT".
func isReject(err error) bool {
	return err != nil && err.Error() == "REJECT"
}

// pageTexts holds the localizable static labels of the error page template.
type pageTexts struct {
	lang       string // "en" or "zh", also used for <html lang>
	bannerOn   string // "on" in the version banner
	details    string // "Request details" info block label
	request    string // "Request" row label
	target     string // "Target" row label
	client     string // "Client" row label
	method     string // "Method" row label
	time       string // "Time" row label
	footPre    string // "Generated on" (prefix of footer)
	footPost   string // "by glider" (suffix of footer)
	titleBlock string // "Request blocked"
	descBlock  string // desc of blocked page
	reasonName string // "Reason" label for blocked reason

	// localized content strings
	titleConnectFail string // "Connect failed"
	descConnectFail  string // "The proxy could not establish a connection..."
	hintConnectFail  string // retry hint for connect failure
	titleWriteFail   string // "Write to upstream failed"
	descWriteFail    string // "The proxy could not forward the request..."
	titleUpstreamEOF string // "Upstream closed connection"
	descUpstreamEOF  string // "The proxy could not read the response..."
	titleBadStart    string // "Invalid upstream response"
	descBadStart     string // "The target server returned an unparseable response start line."
	descBadHeader    string // "The proxy could not read the response headers..."
	titleBadRequest  string // "Bad Request"
	descBadRequest   string // "The proxy could not understand the request..."
	hintBadRequest   string // hint for bad request
}

var enTexts = pageTexts{
	lang:       "en",
	bannerOn:   "on",
	details:    "Request details",
	request:    "Request",
	target:     "Target",
	client:     "Client",
	method:     "Method",
	time:       "Time",
	footPre:    "Generated on",
	footPost:   "by glider",
	titleBlock: "Request blocked",
	descBlock:  "This request was rejected by the proxy rule.",
	reasonName: "Reason",

	titleConnectFail: "Connect failed",
	descConnectFail:  "The proxy could not establish a connection to the remote server.",
	hintConnectFail:  "This is often a temporary failure, so you might just try again. If the problem persists, contact your proxy administrator.",
	titleWriteFail:   "Write to upstream failed",
	descWriteFail:    "The proxy could not forward the request to the target server.",
	titleUpstreamEOF: "Upstream closed connection",
	descUpstreamEOF:  "The proxy could not read the response from the target server.",
	titleBadStart:    "Invalid upstream response",
	descBadStart:     "The target server returned an unparseable response start line.",
	descBadHeader:    "The proxy could not read the response headers from the target server.",
	titleBadRequest:  "Bad Request",
	descBadRequest:   "The proxy could not understand the request sent by the client.",
	hintBadRequest:   "Please check the request and try again.",
}

var zhTexts = pageTexts{
	lang:       "zh",
	bannerOn:   "监听于",
	details:    "请求详情",
	request:    "请求",
	target:     "目标",
	client:     "客户端",
	method:     "方法",
	time:       "时间",
	footPre:    "由 glider 于",
	footPost:   "生成",
	titleBlock: "请求已被拦截",
	descBlock:  "该请求被代理规则拒绝。",
	reasonName: "原因",

	titleConnectFail: "连接失败",
	descConnectFail:  "代理无法与远程服务器建立连接。",
	hintConnectFail:  "这通常是暂时性故障，您可以稍后重试。若问题持续存在，请联系代理管理员。",
	titleWriteFail:   "向目标服务器发送请求失败",
	descWriteFail:    "代理无法将请求转发给目标服务器。",
	titleUpstreamEOF: "目标服务器连接已关闭",
	descUpstreamEOF:  "代理无法从目标服务器读取响应。",
	titleBadStart:    "无效的上游响应",
	descBadStart:     "目标服务器返回了无法解析的响应起始行。",
	descBadHeader:    "代理无法从目标服务器读取响应头。",
	titleBadRequest:  "请求格式错误",
	descBadRequest:   "代理无法理解客户端发送的请求。",
	hintBadRequest:   "请检查请求内容后重试。",
}

// textsFor returns the localized static labels for the given language tag
// (e.g. a value from the Accept-Language request header). Falls back to
// English when the tag is not zh*. q-values (e.g. zh-CN;q=0.8) are honored,
// and languages with q=0 (explicitly not accepted) are skipped.
func textsFor(acceptLang string) pageTexts {
	lang := "en"
	for _, part := range strings.Split(acceptLang, ",") {
		tag := strings.TrimSpace(part)
		q := 1.0
		if idx := strings.Index(tag, ";"); idx >= 0 {
			rest := tag[idx+1:]
			tag = strings.TrimSpace(tag[:idx])
			if qv := strings.TrimPrefix(strings.TrimSpace(rest), "q="); qv != rest {
				fmt.Sscanf(qv, "%f", &q)
			}
		}
		if q <= 0 {
			continue
		}
		if strings.HasPrefix(strings.ToLower(tag), "zh") {
			lang = "zh"
			break
		}
	}
	if lang == "zh" {
		return zhTexts
	}
	return enTexts
}

// errorPage describes a request failure to be rendered as an HTML error page.
// The information layout follows privoxy's built-in error pages
// (status line, software banner, title, request details),
// styled with a Frutiger Aero look (blue-green sky, frosted glass,
// bubbles, clouds, lens flare).
type errorPage struct {
	status  string // HTTP status line, e.g. "502 Bad Gateway"
	version string // software version, e.g. "0.17.0-dev-38b3403"
	listen  string // listen address of the proxy, e.g. "127.0.0.1:18443"
	title   string
	desc    string
	method  string
	request string
	target  string
	client  string
	time    string
	reason  string
	hint    string
	texts   pageTexts // localized static labels
}

const errorPageTemplate = `<!DOCTYPE html>
<html lang="@@LANG@@">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>@@STATUS@@ - glider</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    min-height: 100vh;
    font-family: "Segoe UI", "Segoe UI Semilight", "Arial", sans-serif;
    color: #174a66;
    background:
      radial-gradient(ellipse 80% 55% at 15% 8%, rgba(255,255,255,0.85) 0%, rgba(255,255,255,0) 55%),
      radial-gradient(ellipse 45% 35% at 85% 20%, rgba(255, 240, 190, 0.8) 0%, rgba(255,240,190,0) 60%),
      radial-gradient(ellipse 60% 45% at 90% 75%, rgba(120, 220, 160, 0.6) 0%, rgba(120,220,160,0) 60%),
      linear-gradient(180deg, #6fc3ef 0%, #9fddf6 32%, #bfeef6 55%, #cfeef0 72%, #d6f2d0 100%);
    background-attachment: fixed;
    overflow-x: hidden;
  }
  .cloud { position: fixed; background: rgba(255,255,255,0.9); border-radius: 999px; filter: blur(2px); z-index: 0; animation: cloudDrift 72s ease-in-out infinite alternate; }
  .cloud.c1 { width: 260px; height: 90px; top: 9%; left: 6%; opacity: 0.55; box-shadow: 120px 28px 0 -18px rgba(255,255,255,0.75), 60px -22px 0 -20px rgba(255,255,255,0.6); animation-duration: 54s; }
  .cloud.c2 { width: 320px; height: 100px; top: 30%; right: 4%; opacity: 0.4; box-shadow: -130px 30px 0 -20px rgba(255,255,255,0.65), -60px -30px 0 -24px rgba(255,255,255,0.55); animation-duration: 88s; animation-delay: -30s; }
  @keyframes cloudDrift { from { transform: translateX(-46px); } to { transform: translateX(56px); } }
  .aurora { position: fixed; z-index: 0; border-radius: 50%; filter: blur(72px); pointer-events: none; }
  .aurora.a1 { width: 640px; height: 430px; top: -150px; left: -170px;
    background: radial-gradient(circle, rgba(120, 240, 255, 0.62) 0%, rgba(120, 240, 255, 0) 66%);
    animation: aurora1 26s ease-in-out infinite alternate; }
  .aurora.a2 { width: 560px; height: 420px; top: 24%; right: -190px;
    background: radial-gradient(circle, rgba(140, 255, 190, 0.5) 0%, rgba(140, 255, 190, 0) 66%);
    animation: aurora2 33s ease-in-out infinite alternate; }
  .aurora.a3 { width: 680px; height: 470px; bottom: -230px; left: 20%;
    background: radial-gradient(circle, rgba(110, 190, 255, 0.48) 0%, rgba(110, 190, 255, 0) 64%);
    animation: aurora3 41s ease-in-out infinite alternate; }
  @keyframes aurora1 { from { transform: translate(0, 0) scale(1); } to { transform: translate(72px, 52px) scale(1.18); } }
  @keyframes aurora2 { from { transform: translate(0, 0) scale(1.06); } to { transform: translate(-64px, -42px) scale(0.92); } }
  @keyframes aurora3 { from { transform: translate(0, 0) rotate(-4deg); } to { transform: translate(52px, -58px) rotate(5deg); } }
  .bubble { position: fixed; border-radius: 50%; z-index: 0; }
  .bubble.b1 { width: 90px; height: 90px; left: 10%; bottom: 14%; background: radial-gradient(circle at 30% 28%, rgba(255,255,255,0.95) 0%, rgba(255,255,255,0.15) 18%, rgba(190,240,255,0.12) 40%, rgba(120,210,240,0.28) 68%, rgba(90,190,230,0.18) 100%); box-shadow: inset 0 0 22px rgba(160,230,255,0.55), 0 6px 24px rgba(40,140,190,0.18); }
  .bubble.b2 { width: 56px; height: 56px; left: 20%; bottom: 30%; background: radial-gradient(circle at 32% 30%, rgba(255,255,255,0.95) 0%, rgba(255,255,255,0.1) 20%, rgba(190,240,255,0.1) 45%, rgba(120,210,240,0.22) 100%); box-shadow: inset 0 0 14px rgba(160,230,255,0.5), 0 4px 16px rgba(40,140,190,0.15); }
  .abubble { position: fixed; bottom: -50px; border-radius: 50%; z-index: 0; pointer-events: none;
    background: radial-gradient(circle at 32% 30%, rgba(255,255,255,0.95) 0%, rgba(255,255,255,0.18) 26%, rgba(190,240,255,0.15) 55%, rgba(120,210,240,0.3) 100%);
    box-shadow: inset 0 0 10px rgba(160,230,255,0.55), 0 3px 14px rgba(40,140,190,0.16);
    animation: rise 13s linear infinite; }
  .abubble.ab1 { left: 8%;  width: 26px; height: 26px; animation-duration: 16s; animation-delay: 0s; }
  .abubble.ab2 { left: 21%; width: 16px; height: 16px; animation-duration: 22s; animation-delay: -5s; }
  .abubble.ab3 { left: 34%; width: 30px; height: 30px; animation-duration: 19s; animation-delay: -11s; }
  .abubble.ab4 { left: 48%; width: 14px; height: 14px; animation-duration: 24s; animation-delay: -2s; }
  .abubble.ab5 { left: 60%; width: 34px; height: 34px; animation-duration: 17s; animation-delay: -14s; }
  .abubble.ab6 { left: 72%; width: 18px; height: 18px; animation-duration: 26s; animation-delay: -8s; }
  .abubble.ab7 { left: 86%; width: 22px; height: 22px; animation-duration: 20s; animation-delay: -18s; }
  .abubble.ab8 { left: 94%; width: 12px; height: 12px; animation-duration: 28s; animation-delay: -6s; }
  @keyframes rise {
    0%   { transform: translateY(0); opacity: 0; }
    8%   { opacity: 0.75; }
    60%  { opacity: 0.6; }
    100% { transform: translateY(-108vh); opacity: 0; }
  }
  .flare { position: fixed; width: 480px; height: 480px; top: -140px; right: -120px; border-radius: 50%; z-index: 0;
    background: radial-gradient(circle, rgba(255,255,255,0.95) 0%, rgba(255,255,255,0.35) 18%, rgba(255,255,255,0.08) 40%, rgba(255,255,255,0) 62%);
    filter: blur(2px); opacity: 0.85; }
  .flare::after { content: ""; position: absolute; inset: 8% 18%; border-radius: 50%;
    background: radial-gradient(circle, rgba(255, 250, 210, 0.9) 0%, rgba(255,240,180,0.25) 34%, rgba(255,240,180,0) 60%); }
  .card {
    position: relative; z-index: 2;
    max-width: 640px;
    margin: 60px auto 44px;
    background: rgba(255, 255, 255, 0.38);
    backdrop-filter: blur(14px) saturate(150%);
    -webkit-backdrop-filter: blur(14px) saturate(150%);
    border: 1px solid rgba(255, 255, 255, 0.72);
    border-radius: 22px;
    overflow: hidden;
    box-shadow:
      0 18px 50px rgba(30, 90, 140, 0.28),
      0 3px 12px rgba(255, 255, 255, 0.5) inset,
      0 -6px 18px rgba(120, 210, 250, 0.25) inset;
  }
  .card::before {
    content: ""; position: absolute; top: 0; left: 4%; right: 4%; height: 52%;
    background: linear-gradient(180deg, rgba(255,255,255,0.85) 0%, rgba(255,255,255,0.18) 45%, rgba(255,255,255,0) 100%);
    border-radius: 0 0 50% 50%;
    pointer-events: none;
  }
  .statusbar {
    position: relative; z-index: 1;
    display: flex; align-items: center; justify-content: space-between;
    padding: 14px 26px;
    background: rgba(255, 255, 255, 0.32);
    border-bottom: 1px solid rgba(255, 255, 255, 0.55);
  }
  .statuscode {
    font-size: 30px; font-weight: 700; letter-spacing: 1px;
    background: linear-gradient(160deg, #0c6fb8 0%, #23a6d8 55%, #3fbfb0 100%);
    -webkit-background-clip: text; background-clip: text; color: transparent;
    filter: drop-shadow(0 2px 6px rgba(20, 110, 160, 0.25));
  }
  .banner { font-size: 12px; color: #155a80; text-shadow: 0 1px 0 rgba(255,255,255,0.6); text-align: right; line-height: 1.5; }
  .banner b { font-weight: 600; }
  .mod-title { position: relative; z-index: 1; padding: 26px 30px 6px; }
  .mod-title h1 { font-size: 22px; font-weight: 600; color: #0e5f92; text-shadow: 0 1px 0 rgba(255,255,255,0.7); letter-spacing: 0.5px; }
  .mod-title p { margin-top: 6px; font-size: 13px; color: #1d6d97; }
  .info {
    position: relative; z-index: 1;
    margin: 12px 26px 22px;
    padding: 18px 22px;
    background: rgba(255, 255, 255, 0.45);
    border: 1px solid rgba(255, 255, 255, 0.7);
    border-radius: 14px;
    box-shadow: 0 4px 18px rgba(40, 110, 160, 0.14), inset 0 1px 0 rgba(255,255,255,0.85);
  }
  .info .label { font-size: 11px; text-transform: uppercase; letter-spacing: 1.2px; color: #2a7fb2; margin-bottom: 8px; font-weight: 600; }
  .info .info-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 8px; padding-bottom: 8px; border-bottom: 1px dashed rgba(60, 140, 190, 0.22); }
  .info .info-head .label { margin-bottom: 0; }
  .info .row { display: flex; align-items: baseline; gap: 10px; font-size: 14px; line-height: 1.7; color: #144b6e; word-break: break-all; }
  .info .row + .row { border-top: 1px dashed rgba(60, 140, 190, 0.22); padding-top: 7px; }
  .info .k { flex: 0 0 76px; font-size: 12px; color: #3f8cb8; font-weight: 600; }
  .info .v { flex: 1; text-shadow: 0 1px 0 rgba(255,255,255,0.55); }
  .info .v.url { color: #085fae; font-family: Consolas, "Courier New", monospace; }
  .info .blocked-flag {
    display: inline-block; padding: 4px 10px; white-space: nowrap;
    background: linear-gradient(160deg, #f0b45a 0%, #ea8f3c 100%);
    border-radius: 999px;
    font-size: 11.5px; font-weight: 600; color: #fff;
    text-shadow: 0 1px 2px rgba(160, 90, 20, 0.45);
    box-shadow: 0 2px 8px rgba(220, 130, 40, 0.35);
  }
  .info .reason {
    margin-top: 12px; padding: 9px 12px;
    background: rgba(240, 120, 90, 0.10);
    border-left: 3px solid #e8854f;
    border-radius: 0 8px 8px 0;
    font-size: 12.5px; color: #a3522a; line-height: 1.6;
  }
  .info .hint { margin-top: 10px; font-size: 12.5px; color: #2a6f96; }
  .foot {
    position: relative; z-index: 1;
    padding: 12px 28px 18px;
    font-size: 11.5px; color: #3379a3;
    border-top: 1px solid rgba(255,255,255,0.5);
    background: rgba(255,255,255,0.22);
    text-shadow: 0 1px 0 rgba(255,255,255,0.6);
    line-height: 1.6;
  }
  @media (max-width: 560px) {
    .card { margin: 50px 14px 30px; }
    .statusbar { flex-direction: column; align-items: flex-start; gap: 4px; }
    .banner { text-align: left; }
    .info .blocked-flag { max-width: 55%; overflow: hidden; text-overflow: ellipsis; }
  }
  @media (prefers-reduced-motion: reduce) {
    .cloud, .aurora, .abubble, .flare { animation: none !important; }
  }
</style>
</head>
<body>

<div class="aurora a1"></div>
<div class="aurora a2"></div>
<div class="aurora a3"></div>
<div class="cloud c1"></div>
<div class="cloud c2"></div>
<div class="bubble b1"></div>
<div class="bubble b2"></div>
<div class="abubble ab1"></div>
<div class="abubble ab2"></div>
<div class="abubble ab3"></div>
<div class="abubble ab4"></div>
<div class="abubble ab5"></div>
<div class="abubble ab6"></div>
<div class="abubble ab7"></div>
<div class="abubble ab8"></div>
<div class="flare"></div>

<div class="card">
  <div class="statusbar">
    <div class="statuscode">@@STATUS@@</div>
    <div class="banner"><b>glider @@VERSION@@</b><br>@@BANNER_ON@@ @@LISTEN@@</div>
  </div>
  <div class="mod-title">
    <h1>@@TITLE@@</h1>
    <p>@@DESC@@</p>
  </div>
  <div class="info">
    <div class="info-head">
      <span class="label">@@DETAILS@@</span>
      @@BLOCKED_FLAG@@
    </div>
    <div class="row"><span class="k">@@REQUEST@@</span><span class="v url">@@REQURI@@</span></div>
    <div class="row"><span class="k">@@TARGET@@</span><span class="v">@@TGT@@</span></div>
    <div class="row"><span class="k">@@CLIENT@@</span><span class="v">@@CLI@@</span></div>
    <div class="row"><span class="k">@@METHOD@@</span><span class="v">@@METH@@</span></div>
    <div class="row"><span class="k">@@TIME@@</span><span class="v">@@TM@@</span></div>
    <div class="reason">@@REASON_LABEL@@: @@REASON@@</div>
    <div class="hint">@@HINT@@</div>
  </div>
  <div class="foot">
    @@FOOT_PRE@@ @@TM@@ @@FOOT_POST@@
  </div>
</div>

</body>
</html>`

// sendErrorPage writes a complete HTTP error response with an HTML body.
func sendErrorPage(w io.Writer, proto, status string, ep errorPage) {
	ep.time = time.Now().Format("2006-01-02 15:04:05")
	body := renderErrorPage(ep)

	fmt.Fprintf(w, "%s %s\r\n", proto, status)
	fmt.Fprintf(w, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(w, "Content-Length: %d\r\n", len(body))
	fmt.Fprintf(w, "Connection: close\r\n\r\n")
	io.WriteString(w, body)
}

// sendPlainError writes the original plain-text error response, kept for
// compatibility when the error page feature is disabled.
func sendPlainError(w io.Writer, proto, status string) {
	fmt.Fprintf(w, "%s %s\r\n\r\n", proto, status)
}

// renderErrorPage fills the error page template with escaped field values.
func renderErrorPage(ep errorPage) string {
	if ep.texts.lang == "" {
		ep.texts = enTexts
	}

	blockedFlag := ""
	if ep.title == ep.texts.titleBlock {
		// Show the blocked target host in the flag badge so it differs from
		// the page title and carries actual request detail. Strip a trailing
		// port if present; fall back to the full target on parse failure.
		host := ep.target
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "" {
			blockedFlag = `<span class="blocked-flag" title="` + html.EscapeString(ep.target) + `">` + html.EscapeString(host) + `</span>`
		}
	}

	replacer := strings.NewReplacer(
		"@@LANG@@", html.EscapeString(ep.texts.lang),
		"@@STATUS@@", html.EscapeString(ep.status),
		"@@VERSION@@", html.EscapeString(ep.version),
		"@@LISTEN@@", html.EscapeString(ep.listen),
		"@@BANNER_ON@@", html.EscapeString(ep.texts.bannerOn),
		"@@TITLE@@", html.EscapeString(ep.title),
		"@@DESC@@", html.EscapeString(ep.desc),
		"@@DETAILS@@", html.EscapeString(ep.texts.details),
		"@@BLOCKED_FLAG@@", blockedFlag,
		"@@REQUEST@@", html.EscapeString(ep.texts.request),
		"@@REQURI@@", html.EscapeString(ep.request),
		"@@TARGET@@", html.EscapeString(ep.texts.target),
		"@@TGT@@", html.EscapeString(ep.target),
		"@@CLIENT@@", html.EscapeString(ep.texts.client),
		"@@CLI@@", html.EscapeString(ep.client),
		"@@METHOD@@", html.EscapeString(ep.texts.method),
		"@@METH@@", html.EscapeString(ep.method),
		"@@TIME@@", html.EscapeString(ep.texts.time),
		"@@TM@@", html.EscapeString(ep.time),
		"@@REASON_LABEL@@", html.EscapeString(ep.texts.reasonName),
		"@@REASON@@", html.EscapeString(ep.reason),
		"@@HINT@@", html.EscapeString(ep.hint),
		"@@FOOT_PRE@@", html.EscapeString(ep.texts.footPre),
		"@@FOOT_POST@@", html.EscapeString(ep.texts.footPost),
	)
	return replacer.Replace(errorPageTemplate)
}
