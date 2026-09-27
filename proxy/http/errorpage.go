package http

import (
	"fmt"
	"html"
	"io"
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
	via        string // "Route" row label: the upstream the request was sent over
	stage      string // "Stage" row label: where the request was interrupted
	time       string // "Time" row label
	footPre    string // "Generated on" (prefix of footer)
	footPost   string // "by glider" (suffix of footer)
	titleBlock string // "Request blocked"
	descBlock  string // desc of blocked page

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

	// severity indicator: condition text (shown inside the chip) and the
	// level headline (shown next to the icon). Levels are: error / warning /
	// info. Both are localized.
	condBlock         string // "Rule blocked"
	condConnectFail   string // "Connection failed"
	condInternalError string // "Internal error"
	condUpstreamError string // "Upstream error"
	condBadRequest    string // "Bad request"
	levelError        string // "Error"
	levelWarning      string // "Warning"
	levelInfo         string // "Info"

	// stage names: the processing stage that was interrupted. They fill the
	// "Stage" row, so it is obvious *where* the request died instead of only
	// knowing that something failed.
	stageRequest    string // request parsing failed (bad client request)
	stageDial       string // connecting to the upstream / target
	stageWrite      string // sending the request upstream
	stageRead       string // reading the response from upstream
	stageReadHeader string // reading the response headers from upstream
}

var enTexts = pageTexts{
	lang:       "en",
	bannerOn:   "on",
	details:    "Request details",
	request:    "Request",
	target:     "Target",
	client:     "Client",
	via:        "Route",
	stage:      "Stage",
	time:       "Time",
	footPre:    "Generated on",
	footPost:   "by glider",
	titleBlock: "Request blocked",
	descBlock:  "This request was rejected by the proxy rule.",

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

	condBlock:         "Rule blocked",
	condConnectFail:   "Connection failed",
	condInternalError: "Internal error",
	condUpstreamError: "Upstream error",
	condBadRequest:    "Bad request",
	levelError:        "Error",
	levelWarning:      "Warning",
	levelInfo:         "Info",

	stageRequest:    "Request parse",
	stageDial:       "Connection setup",
	stageWrite:      "Sending request",
	stageRead:       "Reading response",
	stageReadHeader: "Reading headers",
}

var zhTexts = pageTexts{
	lang:       "zh",
	bannerOn:   "监听于",
	details:    "请求详情",
	request:    "请求",
	target:     "目标",
	client:     "客户端",
	via:        "通路",
	stage:      "中断位置",
	time:       "时间",
	footPre:    "由 glider 于",
	footPost:   "生成",
	titleBlock: "请求已被拦截",
	descBlock:  "该请求被代理规则拒绝。",

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

	condBlock:         "规则拦截",
	condConnectFail:   "连接失败",
	condInternalError: "内部错误",
	condUpstreamError: "上游异常",
	condBadRequest:    "请求错误",
	levelError:        "错误",
	levelWarning:      "警告",
	levelInfo:         "信息",

	stageRequest:    "请求解析",
	stageDial:       "建立连接",
	stageWrite:      "发送请求",
	stageRead:       "读取响应",
	stageReadHeader: "读取响应头",
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

// errorSeverity classifies a failure so the indicator chip can be
// color-coded: red for errors, yellow for warnings, blue for info.
type errorSeverity int

const (
	severityError   errorSeverity = iota // red
	severityWarning                      // yellow
	severityInfo                         // blue
)

// errorPage describes a request failure to be rendered as an HTML error page.
// The information layout follows privoxy's built-in error pages
// (status line, software banner, title, request details),
// styled with a Frutiger Aero look (blue-green sky, frosted glass,
// bubbles, clouds, lens flare).
type errorPage struct {
	status  string // HTTP status line, e.g. "502 Bad Gateway"
	version string // software version, e.g. "0.17.0"
	listen  string // listen address of the proxy, e.g. "127.0.0.1:18443"
	title   string
	desc    string
	request string
	target  string
	client  string
	// via is the route the request was actually sent over: the upstream
	// address reported by the chosen dialer (e.g. "DIRECT", "127.0.0.1:8118"
	// or "a,b" for a dialer chain). Empty when the failure happened before a
	// route had been picked; the row is then omitted.
	via string
	// stage is the localized name of the processing stage that was
	// interrupted ("Connection setup", "Reading response", ...). It answers
	// "where did it break", which the reason alone never told us.
	stage string
	time  string
	// reason is the machine-facing cause (e.g. "REJECT", a dial error).
	// It is rendered *inside* the indicator chip so the chip carries a real
	// message instead of repeating the class label. When empty, the chip
	// falls back to the condition text.
	reason string
	// condition is the localized class label ("Rule blocked", "Connection
	// failed", ...). It is the chip fallback text and is exposed to assistive
	// technology via the chip's tooltip.
	condition string
	// severity drives the chip color: error (red), warning (yellow), info (blue).
	severity errorSeverity
	// hint is the advisory line shown below the details.
	hint  string
	texts pageTexts // localized static labels
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
  /* header: reason chip on the left, severity indicator pinned to the right
     (mirrors the original layout where the category badge sat on the right). */
  .info .info-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; padding-bottom: 8px; border-bottom: 1px dashed rgba(60, 140, 190, 0.22); }
  .info .info-head .ind-chip { margin-left: 0; margin-right: auto; }
  .info .info-head .indicator { margin-left: auto; }
  .info .row { display: flex; align-items: baseline; gap: 10px; font-size: 14px; line-height: 1.7; color: #144b6e; word-break: break-all; }
  .info .row + .row { border-top: 1px dashed rgba(60, 140, 190, 0.22); padding-top: 7px; }
  .info .k { flex: 0 0 76px; font-size: 12px; color: #3f8cb8; font-weight: 600; }
  .info .v { flex: 1; text-shadow: 0 1px 0 rgba(255,255,255,0.55); }
  .info .v.url { color: #085fae; font-family: Consolas, "Courier New", monospace; }
  /* severity indicator: a Frutiger Aero glass bead + level name, pinned to
     the right of the details header. Frosted translucent shell, glossy
     specular highlight, soft colored glow — red = error, yellow = warning,
     blue = info. */
  .indicator {
    position: relative; overflow: hidden;
    display: inline-flex; align-items: center; gap: 7px;
    padding: 3px 12px 3px 4px; white-space: nowrap;
    border-radius: 999px;
    font-size: 11.5px; font-weight: 700; letter-spacing: 0.3px;
    border: 1px solid transparent;
    vertical-align: middle; max-width: 100%;
    backdrop-filter: blur(10px) saturate(160%);
    -webkit-backdrop-filter: blur(10px) saturate(160%);
    text-shadow: 0 1px 0 rgba(255,255,255,0.55);
  }
  /* glossy top highlight, matching the card's glass treatment */
  .indicator::before {
    content: ""; position: absolute; left: 3px; right: 3px; top: 1px; height: 52%;
    border-radius: 999px 999px 60% 60% / 999px 999px 100% 100%;
    background: linear-gradient(180deg, rgba(255,255,255,0.8) 0%, rgba(255,255,255,0.16) 60%, rgba(255,255,255,0) 100%);
    pointer-events: none;
  }
  .indicator .ind-icon, .indicator .ind-text { position: relative; z-index: 1; }
  .indicator .ind-icon {
    width: 19px; height: 19px; flex-shrink: 0;
    display: inline-flex; align-items: center; justify-content: center;
    border-radius: 50%; color: #fff;
  }
  /* specular glass bead: inner top-left light + bottom rim + inner shadow */
  .indicator .ind-icon::after {
    content: ""; position: absolute; inset: 1px; border-radius: 50%;
    background: radial-gradient(circle at 32% 26%, rgba(255,255,255,0.95) 0%, rgba(255,255,255,0.35) 26%, rgba(255,255,255,0) 56%);
    pointer-events: none;
  }
  .indicator .ind-icon svg { width: 12px; height: 12px; display: block; position: relative; z-index: 1; }
  .indicator .ind-text { overflow: hidden; text-overflow: ellipsis; }
  .indicator-level-error {
    color: #8f1f18;
    background: linear-gradient(180deg, rgba(255,255,255,0.6) 0%, rgba(255,235,232,0.5) 45%, rgba(240,150,140,0.32) 100%);
    border-color: rgba(255,255,255,0.75);
    box-shadow: 0 2px 8px rgba(192,57,43,0.28), inset 0 -3px 8px rgba(192,57,43,0.16);
  }
  .indicator-level-error .ind-icon {
    background: radial-gradient(circle at 32% 26%, #ff9d8f 0%, #e0503f 48%, #b3342a 100%);
    box-shadow: 0 2px 6px rgba(192,57,43,0.45), inset 0 -2px 4px rgba(120,20,10,0.35), inset 0 1px 1px rgba(255,255,255,0.6);
  }
  .indicator-level-warning {
    color: #7a5c00;
    background: linear-gradient(180deg, rgba(255,255,255,0.62) 0%, rgba(255,250,225,0.5) 45%, rgba(245,215,110,0.34) 100%);
    border-color: rgba(255,255,255,0.78);
    box-shadow: 0 2px 8px rgba(190,150,0,0.26), inset 0 -3px 8px rgba(190,150,0,0.16);
  }
  .indicator-level-warning .ind-icon {
    background: radial-gradient(circle at 32% 26%, #ffe98a 0%, #f1c40f 50%, #c99a00 100%);
    color: #6b5200;
    box-shadow: 0 2px 6px rgba(190,150,0,0.42), inset 0 -2px 4px rgba(140,105,0,0.3), inset 0 1px 1px rgba(255,255,255,0.7);
  }
  .indicator-level-info {
    color: #14527f;
    background: linear-gradient(180deg, rgba(255,255,255,0.62) 0%, rgba(235,247,255,0.5) 45%, rgba(140,205,245,0.34) 100%);
    border-color: rgba(255,255,255,0.78);
    box-shadow: 0 2px 8px rgba(41,128,185,0.26), inset 0 -3px 8px rgba(41,128,185,0.16);
  }
  .indicator-level-info .ind-icon {
    background: radial-gradient(circle at 32% 26%, #8fd4ff 0%, #3498db 50%, #1f6fa8 100%);
    box-shadow: 0 2px 6px rgba(41,128,185,0.42), inset 0 -2px 4px rgba(20,80,130,0.32), inset 0 1px 1px rgba(255,255,255,0.65);
  }
  /* left-hand message chip in the details header, carrying the real failure
     reason. Frosted glass inset panel to stay in the Aero idiom. */
  .info .ind-chip {
    display: inline-block; min-width: 0;
    padding: 3px 11px;
    border-radius: 8px;
    background: linear-gradient(180deg, rgba(255,255,255,0.62) 0%, rgba(255,255,255,0.42) 100%);
    border: 1px solid rgba(255,255,255,0.7);
    box-shadow: 0 2px 8px rgba(40,110,160,0.12), inset 0 1px 1px rgba(255,255,255,0.9);
    font-size: 11.5px; color: #22658c;
    font-family: Consolas, "Courier New", monospace;
    overflow-wrap: break-word; word-break: break-word;
    vertical-align: middle;
  }
  @media (prefers-reduced-motion: reduce) {
    .indicator, .indicator::before { transition: none; }
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
    .info .info-head { flex-wrap: wrap; }
    .info .info-head .ind-chip { order: 2; margin-right: 0; }
    .info .info-head .indicator { order: 1; margin-left: 0; }
    .indicator { max-width: 100%; }
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
      @@REASON_CHIP@@
      @@INDICATOR@@
    </div>
    <div class="row"><span class="k">@@REQUEST@@</span><span class="v url">@@REQURI@@</span></div>
    <div class="row"><span class="k">@@TARGET@@</span><span class="v">@@TGT@@</span></div>
    <div class="row"><span class="k">@@CLIENT@@</span><span class="v">@@CLI@@</span></div>
    @@VIA_ROW@@
    @@STAGE_ROW@@
    <div class="row"><span class="k">@@TIME@@</span><span class="v">@@TM@@</span></div>
    <div class="hint">@@HINT@@</div>
  </div>
  <div class="foot">
    @@FOOT_PRE@@ @@TM@@ @@FOOT_POST@@
  </div>
</div>

</body>
</html>`

// Inline SVG icons for the severity indicator (12x12 viewBox, white/currentColor).
// A cross for errors, a triangle-exclamation for warnings, and an "i" for info.
const svgIconError = `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><line x1="4.5" y1="4.5" x2="11.5" y2="11.5"/><line x1="11.5" y1="4.5" x2="4.5" y2="11.5"/></svg>`

const svgIconWarning = `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"><path d="M8 1.8L15 14H1L8 1.8z"/><line x1="8" y1="6.4" x2="8" y2="10" stroke-width="2" stroke-linecap="round"/><circle cx="8" cy="12.1" r="0.85" fill="currentColor" stroke="none"/></svg>`

const svgIconInfo = `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="8" cy="8" r="6.6"/><circle cx="8" cy="4.7" r="0.95" fill="currentColor" stroke="none"/><line x1="8" y1="7.2" x2="8" y2="11.6" stroke-width="2.1" stroke-linecap="round"/></svg>`

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

// renderIndicator builds the severity indicator pill: a color-coded icon
// plus the level name (Error / Warning / Info). It is pinned to the right
// edge of the details header, mirroring where the old category badge sat.
// Color follows severity: red = error, yellow = warning, blue = info.
func renderIndicator(ep errorPage) string {
	var levelClass, levelText, icon string
	switch ep.severity {
	case severityWarning:
		levelClass, levelText, icon = "indicator-level-warning", ep.texts.levelWarning, svgIconWarning
	case severityInfo:
		levelClass, levelText, icon = "indicator-level-info", ep.texts.levelInfo, svgIconInfo
	default:
		levelClass, levelText, icon = "indicator-level-error", ep.texts.levelError, svgIconError
	}

	return `<span class="indicator ` + levelClass + `" title="` + html.EscapeString(ep.condition) + `">` +
		`<span class="ind-icon">` + icon + `</span><span class="ind-text">` +
		html.EscapeString(levelText) + `</span></span>`
}

// renderReasonChip builds the left-hand message chip, which carries the
// actual failure reason (falling back to the condition label when there is
// no machine-readable reason). It replaces the former static "Request
// details" label so the header reads [reason] ..... [indicator].
func renderReasonChip(ep errorPage) string {
	chip := strings.TrimSpace(ep.reason)
	if chip == "" {
		chip = ep.condition
	}
	return `<span class="ind-chip" title="` + html.EscapeString(chip) + `">` +
		html.EscapeString(truncateRunes(chip, 120)) + `</span>`
}

// renderInfoRow renders one "label | value" row of the request details block.
// It returns an empty string when the value is blank, so the optional
// diagnostic rows (route, stage) disappear instead of leaving an empty line
// behind. Values are escaped, and long ones wrap thanks to the row CSS.
func renderInfoRow(label, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return `<div class="row"><span class="k">` + html.EscapeString(label) +
		`</span><span class="v">` + html.EscapeString(value) + `</span></div>`
}

// truncateRunes shortens s to at most n runes, appending an ellipsis when it
// was cut. It is rune-aware so multi-byte text (e.g. Chinese) is not broken.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\u2026"
}

// renderErrorPage fills the error page template with escaped field values.
func renderErrorPage(ep errorPage) string {
	if ep.texts.lang == "" {
		ep.texts = enTexts
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
		"@@REASON_CHIP@@", renderReasonChip(ep),
		"@@INDICATOR@@", renderIndicator(ep),
		"@@REQUEST@@", html.EscapeString(ep.texts.request),
		"@@REQURI@@", html.EscapeString(ep.request),
		"@@TARGET@@", html.EscapeString(ep.texts.target),
		"@@TGT@@", html.EscapeString(ep.target),
		"@@CLIENT@@", html.EscapeString(ep.texts.client),
		"@@CLI@@", html.EscapeString(ep.client),
		"@@VIA_ROW@@", renderInfoRow(ep.texts.via, ep.via),
		"@@STAGE_ROW@@", renderInfoRow(ep.texts.stage, ep.stage),
		"@@TIME@@", html.EscapeString(ep.texts.time),
		"@@TM@@", html.EscapeString(ep.time),
		"@@HINT@@", html.EscapeString(ep.hint),
		"@@FOOT_PRE@@", html.EscapeString(ep.texts.footPre),
		"@@FOOT_POST@@", html.EscapeString(ep.texts.footPost),
	)
	return replacer.Replace(errorPageTemplate)
}
