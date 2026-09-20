package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// registerHandlers 注册所有 HTTP 路由
func registerHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/proxy.pac", handlePAC)
	mux.HandleFunc("/wpad.dat", handlePAC)
	mux.HandleFunc("/ca.crt", handleCA)
	mux.HandleFunc("/install/win.ps1", handleWinPS1)
	mux.HandleFunc("/install/win.bat", handleWinBAT)
	mux.HandleFunc("/install/linux.sh", handleLinuxSH)
	mux.HandleFunc("/install/macos.sh", handleMacOSSH)
	mux.HandleFunc("/install/ios.mobileconfig", handleIOS)
	mux.HandleFunc("/install/android.html", handleAndroidGuide)
	mux.HandleFunc("/", handleIndex)
}

// handlePAC 返回 PAC 文件
func handlePAC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
	w.Header().Set("Cache-Control", "no-cache")

	cfg := getConfig()
	fmt.Fprint(w, generatePAC(cfg))
}

// generatePAC 根据当前配置生成 PAC 文件内容
func generatePAC(cfg *Config) string {
	var b strings.Builder
	b.WriteString("// PAC 文件 - 由 pacweb 自动生成\n")
	b.WriteString("// 修改时间: " + logTimestamp() + "\n\n")
	b.WriteString("function FindProxyForURL(url, host) {\n")

	// 内网直连规则
	b.WriteString("  // 内网直连\n")
	b.WriteString("  if (isPlainHostName(host) ||\n")
	b.WriteString("      dnsDomainIs(host, \".local\") ||\n")
	b.WriteString("      dnsDomainIs(host, \".lan\")) {\n")
	b.WriteString("    return \"DIRECT\";\n")
	b.WriteString("  }\n\n")

	// CIDR 直连规则
	for _, cidr := range cfg.DirectRanges {
		ip, mask, err := cidrToPAC(cidr)
		if err != nil {
			continue
		}
		b.WriteString(fmt.Sprintf("  if (isInNet(host, \"%s\", \"%s\")) return \"DIRECT\";\n", ip, mask))
	}

	// 外网走代理 + 故障转移
	b.WriteString("\n  // 外网走代理，故障时直连\n")
	b.WriteString(fmt.Sprintf("  return \"PROXY %s; DIRECT\";\n", cfg.ProxyAddr))
	b.WriteString("}\n")

	return b.String()
}

// cidrToPAC 将 CIDR 转换为 PAC 的 IP + mask 格式
func cidrToPAC(cidr string) (string, string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", err
	}
	mask := fmt.Sprintf("%d.%d.%d.%d", ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])
	return ipNet.IP.String(), mask, nil
}

// handleCA 返回 CA 证书文件
func handleCA(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	certPath := filepath.Join(exeDir, cfg.CACertPath)

	data, err := os.ReadFile(certPath)
	if err != nil {
		http.Error(w, "证书文件未找到，请检查 auto_export_ca 配置", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", "attachment; filename=\"ca.crt\"")
	w.Write(data)
}

// handleWinPS1 返回 Windows PowerShell 安装脚本
func handleWinPS1(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	host := getListenHost(r, cfg)

	script := fmt.Sprintf(`# 设置控制台代码页为 UTF-8，确保中文输出正常
chcp 65001 > $null
$ErrorActionPreference = 'Stop'

# 自动提升权限（非管理员时弹出 UAC 请求）
$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "正在请求管理员权限..."
    Start-Process powershell -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File',$PSCommandPath) -Verb RunAs
    exit
}

$certUrl = 'http://%s/ca.crt'
$tmpCert = "$env:TEMP\adguard-ca.crt"

Write-Host "[1/3] 正在下载根证书..."
try {
    Invoke-WebRequest -Uri $certUrl -OutFile $tmpCert -UseBasicParsing
} catch {
    Write-Host "证书下载失败: $_" -ForegroundColor Red
    Pause
    exit 1
}

Write-Host "[2/3] 正在安装到受信任的根证书颁发机构..."
& certutil.exe -addstore -f Root $tmpCert
if ($LASTEXITCODE -eq 0) {
    Write-Host "[3/3] 证书安装成功！" -ForegroundColor Green
} else {
    Write-Host "证书安装失败，请尝试手动导入。" -ForegroundColor Red
}

Write-Host ""
Write-Host "PAC 代理配置地址: http://%s/proxy.pac"
Write-Host "在浏览器或系统代理设置中使用此 URL 即可自动配置代理。"
Remove-Item $tmpCert -ErrorAction SilentlyContinue
Pause
`, host, host)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"install.ps1\"")
	// 写入 UTF-8 BOM，确保 PowerShell 5 按 UTF-8 解析脚本中的中文
	w.Write([]byte{0xEF, 0xBB, 0xBF})
	fmt.Fprint(w, script)
}

// handleWinBAT 返回 Windows 批处理启动脚本（自动提权 + 调用 ps1）
func handleWinBAT(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	host := getListenHost(r, cfg)

	script := fmt.Sprintf(`@echo off
setlocal enabledelayedexpansion

REM === Self-contained Windows installer (ASCII-only to avoid encoding issues) ===

REM 1. Self-elevate to administrator (UAC)
net session >nul 2>&1
if !errorLevel! neq 0 (
    echo Requesting administrator privileges...
    powershell -NoProfile -Command "Start-Process '%%~f0' -Verb RunAs"
    exit /b
)

set "CERTHOST=http://%s"
set "CERTURL=!CERTHOST!/ca.crt"
set "PACURL=!CERTHOST!/proxy.pac"
set "CERTFILE=%%TEMP%%\adguard-ca.crt"

echo.
echo ============================================
echo   LAN Proxy CA Certificate Installer
echo ============================================
echo.

echo [1/3] Downloading CA certificate...
certutil -urlcache -split -f "!CERTURL!" "!CERTFILE!" >nul 2>&1
if not exist "!CERTFILE!" (
    echo   certutil failed, trying PowerShell...
    powershell -NoProfile -Command "Invoke-WebRequest -Uri '!CERTURL!' -OutFile '!CERTFILE!' -UseBasicParsing"
)
if not exist "!CERTFILE!" (
    echo [ERROR] Failed to download certificate.
    pause
    exit /b 1
)

echo [2/3] Installing certificate to Trusted Root...
certutil -addstore -f Root "!CERTFILE!"
if !errorLevel! neq 0 (
    echo [ERROR] Certificate installation failed.
    pause
    exit /b 1
)

echo [3/3] Setting system PAC proxy...
powershell -NoProfile -Command "Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -Name AutoConfigURL -Value '!PACURL!'"

echo.
echo ============================================
echo   Installation complete!
echo   PAC URL: !PACURL!
echo   Please restart your browser to apply.
echo ============================================
echo.

del "!CERTFILE!" >nul 2>&1
pause
`, host)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"install.bat\"")
	fmt.Fprint(w, script)
}

// handleLinuxSH 返回 Linux 安装脚本
func handleLinuxSH(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	host := getListenHost(r, cfg)

	script := fmt.Sprintf(`#!/bin/bash
set -e
CERT_URL="http://%s/ca.crt"
PAC_URL="http://%s/proxy.pac"
CERT_FILE=$(mktemp --suffix=.crt)

echo "[1/4] 下载根证书..."
curl -fsSL "$CERT_URL" -o "$CERT_FILE"

echo "[2/4] 安装证书到系统信任库..."
if command -v update-ca-certificates >/dev/null 2>&1; then
    sudo cp "$CERT_FILE" /usr/local/share/ca-certificates/adguard-ca.crt
    sudo update-ca-certificates
elif command -v update-ca-trust >/dev/null 2>&1; then
    sudo cp "$CERT_FILE" /etc/pki/ca-trust/source/anchors/adguard-ca.crt
    sudo update-ca-trust extract
else
    echo "无法识别的 Linux 发行版，请手动安装证书。"
    exit 1
fi

echo "[3/4] 清理临时文件..."
rm -f "$CERT_FILE"

echo "[4/4] 安装完成！"
echo ""
echo "PAC 代理配置地址: $PAC_URL"
echo "在浏览器或系统代理设置中使用此 URL 即可自动配置代理。"
echo ""
echo "如使用 Firefox，需在 about:preferences#privacy -> 证书 -> 查看证书 -> 导入 手动导入。"
`, host, host)

	w.Header().Set("Content-Type", "application/x-sh")
	w.Header().Set("Content-Disposition", "attachment; filename=\"install.sh\"")
	fmt.Fprint(w, script)
}

// handleMacOSSH 返回 macOS 安装脚本
func handleMacOSSH(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	host := getListenHost(r, cfg)

	script := fmt.Sprintf(`#!/bin/bash
set -e
CERT_URL="http://%s/ca.crt"
PAC_URL="http://%s/proxy.pac"
CERT_FILE="/tmp/adguard-ca.crt"

echo "[1/3] 下载根证书..."
curl -fsSL "$CERT_URL" -o "$CERT_FILE"

echo "[2/3] 安装证书到系统钥匙串（需要管理员密码）..."
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$CERT_FILE"

echo "[3/3] 安装完成！"
echo ""
echo "PAC 代理配置地址: $PAC_URL"
echo "在 系统设置 -> 网络 -> Wi-Fi -> 详细信息 -> 代理 -> 自动代理配置 中填入上述 URL。"
rm -f "$CERT_FILE"
`, host, host)

	w.Header().Set("Content-Type", "application/x-sh")
	w.Header().Set("Content-Disposition", "attachment; filename=\"install.sh\"")
	fmt.Fprint(w, script)
}

// handleIOS 动态生成 iOS 描述文件 (.mobileconfig)
func handleIOS(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	certPath := filepath.Join(exeDir, cfg.CACertPath)

	// 读取证书文件
	certData, err := os.ReadFile(certPath)
	if err != nil {
		http.Error(w, "证书文件未找到", http.StatusInternalServerError)
		return
	}

	// 如果是 PEM 格式，提取 base64 部分
	certBase64 := extractPEMBase64(string(certData))
	if certBase64 == "" {
		// 如果不是 PEM，直接 base64 编码
		certBase64 = base64.StdEncoding.EncodeToString(certData)
	}

	// 生成 UUID
	uuid := generateUUID()

	mobileconfig := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>PayloadContent</key>
  <array>
    <dict>
      <key>PayloadType</key>
      <string>com.apple.security.root</string>
      <key>PayloadIdentifier</key>
      <string>lan.pacweb.ca</string>
      <key>PayloadVersion</key>
      <integer>1</integer>
      <key>PayloadContent</key>
      <data>%s</data>
    </dict>
  </array>
  <key>PayloadDisplayName</key>
  <string>%s</string>
  <key>PayloadIdentifier</key>
  <string>lan.pacweb</string>
  <key>PayloadType</key>
  <string>Configuration</string>
  <key>PayloadUUID</key>
  <string>%s</string>
  <key>PayloadVersion</key>
  <integer>1</integer>
</dict>
</plist>
`, certBase64, cfg.CADisplayName, uuid)

	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", "attachment; filename=\"ca.mobileconfig\"")
	fmt.Fprint(w, mobileconfig)
}

// handleAndroidGuide 返回 Android 证书安装引导页
func handleAndroidGuide(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	host := getListenHost(r, cfg)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Android 证书安装引导</title>
<style>
body { font-family: -apple-system, sans-serif; max-width: 680px; margin: 0 auto; padding: 20px; line-height: 1.6; color: #333; overflow-wrap: break-word; word-break: break-word; }
h1 { font-size: 1.5em; border-bottom: 2px solid #4CAF50; padding-bottom: 10px; }
.step { background: #f5f5f5; border-left: 4px solid #4CAF50; padding: 15px; margin: 15px 0; border-radius: 4px; }
.step-num { display: inline-block; background: #4CAF50; color: white; width: 28px; height: 28px; text-align: center; line-height: 28px; border-radius: 50%; margin-right: 10px; font-weight: bold; }
.note { background: #fff3cd; border: 1px solid #ffcda1; padding: 12px; border-radius: 4px; margin: 15px 0; }
.note ul { margin: 8px 0; padding-left: 20px; }
.note li { margin: 6px 0; line-height: 1.6; }
.download-btn { display: inline-block; background: #4CAF50; color: white; padding: 12px 30px; text-decoration: none; border-radius: 5px; font-size: 1.1em; margin: 10px 0; }
.pac-url { background: #e8f5e9; padding: 10px; border-radius: 4px; font-family: monospace; word-break: break-all; }
</style>
</head>
<body>
<h1>Android 证书安装引导</h1>

<div class="step">
<span class="step-num">1</span>
<strong>下载根证书</strong><br><br>
<a class="download-btn" href="/ca.crt">下载 CA 证书</a>
</div>

<div class="step">
<span class="step-num">2</span>
<strong>安装证书</strong><br><br>
打开「设置」→「安全」→「加密与凭据」→「安装证书」→「CA 证书」<br>
选择刚下载的 ca.crt 文件。<br><br>
<div class="note">
注意：不同厂商的路径可能不同，常见路径：
<ul>
<li>小米/Redmi：设置 → 密码与安全 → 系统安全 → 加密与凭据 → 安装证书</li>
<li>华为/荣耀：设置 → 安全 → 更多安全设置 → 加密与凭据 → 从存储设备安装</li>
<li>OPPO/vivo：设置 → 其他设置 → 设备与隐私 → 从存储设备安装</li>
<li>原生 Android：设置 → 安全 → 加密与凭据 → 安装证书 → CA 证书</li>
</ul>
</div>
</div>

<div class="step">
<span class="step-num">3</span>
<strong>配置代理</strong><br><br>
PAC 自动配置地址：<br>
<div class="pac-url">http://%s/proxy.pac</div><br><br>
长按当前连接的 Wi-Fi → 修改网络 → 高级选项 → 代理 → 设为「自动配置」<br>
填入上面的 PAC URL，保存。
</div>

<div class="note">
<strong>重要提示：</strong><br>
Android 7+ 的应用默认不信任用户安装的 CA 证书。浏览器会信任，但其他 App 可能不信任。<br>
如果需要所有 App 都信任（如抓包），需要 Root 后将证书安装到系统证书库。<br>
推荐使用 Magisk 模块「MagiskTrustUserCerts」自动将用户证书转为系统证书。
</div>

<div style="text-align:center; margin-top:30px;">
<a href="/">返回首页</a>
</div>
</body>
</html>`, host)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// handleIndex 引导首页（自动识别客户端系统）
func handleIndex(w http.ResponseWriter, r *http.Request) {
	// 如果请求的不是根路径，返回 404
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}

	data := indexData{
		ServerIP: getListenHost(r, getConfig()),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl := template.Must(template.New("index").Parse(indexHTML))
	tmpl.Execute(w, data)
}

// getListenHost 从请求中获取服务器的 host:port
func getListenHost(r *http.Request, cfg *Config) string {
	// 优先使用请求的 Host 头
	if r.Host != "" {
		return r.Host
	}
	// 后备使用配置中的监听地址
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return cfg.Listen
	}
	if host == "" || host == ":" {
		host = "192.168.155.22"
	}
	return net.JoinHostPort(host, port)
}

// extractPEMBase64 从 PEM 格式中提取 base64 内容
func extractPEMBase64(pem string) string {
	// 去掉 PEM 头尾
	pem = strings.TrimSpace(pem)
	if !strings.HasPrefix(pem, "-----BEGIN") {
		return ""
	}
	// 找到 BEGIN 和 END 之间的内容
	start := strings.Index(pem, "\n")
	end := strings.Index(pem, "-----END")
	if start == -1 || end == -1 {
		return ""
	}
	content := strings.TrimSpace(pem[start+1 : end])
	// 去掉换行符
	content = strings.ReplaceAll(content, "\n", "")
	content = strings.ReplaceAll(content, "\r", "")
	return content
}

// generateUUID 生成随机 UUID
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// logTimestamp 返回当前时间字符串
func logTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// indexData 首页模板数据
type indexData struct {
	ServerIP string
}

// indexHTML 首页 HTML 模板
const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>局域网代理与证书配置</title>
<style>
body { font-family: -apple-system, "Microsoft YaHei", sans-serif; max-width: 780px; margin: 0 auto; padding: 20px; line-height: 1.6; color: #333; background: #f0f2f5; overflow-wrap: break-word; }
.container { background: white; border-radius: 12px; padding: 30px; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
h1 { font-size: 1.8em; color: #1a73e8; border-bottom: 2px solid #1a73e8; padding-bottom: 10px; }
.platform { display: none; margin: 20px 0; }
.platform.active { display: block; }
.btn { display: inline-block; background: #1a73e8; color: white; padding: 12px 28px; text-decoration: none; border-radius: 6px; font-size: 1em; margin: 8px 8px 8px 0; transition: background 0.2s; }
.btn:hover { background: #1557b0; }
.btn.green { background: #4CAF50; }
.btn.green:hover { background: #3d8b40; }
.btn.orange { background: #ff9800; }
.btn.orange:hover { background: #e68a00; }
.pac-url { background: #e8f0fe; padding: 12px; border-radius: 6px; font-family: monospace; word-break: break-all; margin: 10px 0; }
.detection { background: #fff3cd; border: 1px solid #ffcda1; padding: 12px; border-radius: 6px; margin: 15px 0; }
.steps { counter-reset: step; }
.steps .step { counter-increment: step; margin: 15px 0; padding-left: 40px; position: relative; }
.steps .step::before { content: counter(step); position: absolute; left: 0; top: 0; background: #1a73e8; color: white; width: 28px; height: 28px; text-align: center; line-height: 28px; border-radius: 50%; font-weight: bold; }
.note { background: #fff3cd; border: 1px solid #ffcda1; padding: 12px; border-radius: 6px; margin: 15px 0; font-size: 0.9em; }
.footer { text-align: center; margin-top: 30px; color: #666; font-size: 0.85em; }
code { background: #f1f1f1; padding: 2px 6px; border-radius: 3px; font-family: monospace; }
pre { background: #f1f1f1; padding: 12px; border-radius: 6px; overflow-x: auto; overflow-wrap: break-word; word-break: break-all; font-size: 0.9em; }
</style>
</head>
<body>
<div class="container">
<h1>局域网代理与证书配置</h1>

<div class="detection" id="detection">正在检测系统类型...</div>

<!-- Windows -->
<div class="platform" id="windows">
<h2>Windows</h2>
<div class="steps">
<div class="step">下载并双击运行安装脚本，自动请求管理员权限、安装证书并配置 PAC 代理：</div>
</div>
<a class="btn green" href="/install/win.bat">下载并运行一键安装脚本</a>
<div class="steps" style="margin-top:20px;">
<div class="step">脚本执行完成后，重启浏览器即可生效。PAC 地址：</div>
</div>
<div class="pac-url">http://{{.ServerIP}}/proxy.pac</div>
<div class="note">Firefox 需在 about:preferences#privacy → 网络设置 → 使用系统代理，或单独配置「自动代理配置 URL」。</div>
</div>

<!-- Linux -->
<div class="platform" id="linux">
<h2>Linux</h2>
<div class="steps">
<div class="step">下载并运行安装脚本：</div>
</div>
<pre>curl -fsSL http://{{.ServerIP}}/install/linux.sh | sudo bash</pre>
<div class="steps" style="margin-top:20px;">
<div class="step">在系统或浏览器代理设置中填入 PAC URL：</div>
</div>
<div class="pac-url">http://{{.ServerIP}}/proxy.pac</div>
<div class="note">Firefox on Linux 有独立的证书库，需在 about:preferences#privacy → 证书 → 查看证书 → 导入手动安装。</div>
</div>

<!-- iOS -->
<div class="platform" id="ios">
<h2>iOS (iPhone/iPad)</h2>
<div class="steps">
<div class="step">用 <strong>Safari</strong> 点击下方按钮下载描述文件：</div>
</div>
<a class="btn green" href="/install/ios.mobileconfig">安装描述文件</a>
<div class="steps" style="margin-top:20px;">
<div class="step">前往「设置 → 已下载描述文件 → 安装」（输入锁屏密码）</div>
<div class="step"><strong>关键步骤：</strong>前往「设置 → 通用 → 关于本机 → 证书信任设置 → 开启 CA 证书的完全信任」</div>
<div class="step">在「设置 → Wi-Fi → 当前 Wi-Fi → 配置代理 → 自动」填入：</div>
</div>
<div class="pac-url">http://{{.ServerIP}}/proxy.pac</div>
<div class="note">必须使用 Safari 下载描述文件。Chrome/Firefox on iOS 无法触发描述文件安装。</div>
</div>

<!-- Android -->
<div class="platform" id="android">
<h2>Android</h2>
<p>Android 的证书安装需要手动操作，<a href="/install/android.html">点击查看详细图文引导</a>。</p>
<div class="steps" style="margin-top:20px;">
<div class="step">在 Wi-Fi 代理设置中使用 PAC 自动配置：</div>
</div>
<div class="pac-url">http://{{.ServerIP}}/proxy.pac</div>
<div class="note">Android 7+ 的应用默认不信任用户 CA 证书。浏览器可正常使用，其他 App 需要额外处理。</div>
</div>

<!-- macOS -->
<div class="platform" id="macos">
<h2>macOS</h2>
<div class="steps">
<div class="step">运行一键安装脚本（需输入管理员密码）：</div>
</div>
<pre>curl -fsSL http://{{.ServerIP}}/install/macos.sh | bash</pre>
<div class="steps" style="margin-top:20px;">
<div class="step">或手动下载证书安装：</div>
</div>
<a class="btn" href="/ca.crt">下载 CA 证书</a>
<div class="steps" style="margin-top:20px;">
<div class="step">双击 ca.crt → 打开「钥匙串访问」→ 找到该证书 → 双击 → 展开「信任」→ 设为「始终信任」</div>
<div class="step">在「系统设置 → 网络 → Wi-Fi → 详细信息 → 代理 → 自动代理配置」填入：</div>
</div>
<div class="pac-url">http://{{.ServerIP}}/proxy.pac</div>
</div>

<!-- 通用 -->
<div class="platform" id="unknown">
<h2>未识别的系统</h2>
<p>请手动下载证书并安装：</p>
<a class="btn" href="/ca.crt">下载 CA 证书</a>
<p style="margin-top:20px;">PAC 代理配置地址：</p>
<div class="pac-url">http://{{.ServerIP}}/proxy.pac</div>
</div>

<div class="footer">
<p>pacweb - 局域网 PAC 与证书托管服务</p>
</div>
</div>

<script>
(function() {
  var ua = navigator.userAgent;
  var platform = 'unknown';
  
  // 注意检测顺序：Android 必须在 Linux 之前
  if (/iPad|iPhone|iPod/.test(ua)) {
    platform = 'ios';
  } else if (/Android/.test(ua)) {
    platform = 'android';
  } else if (/Windows/.test(ua)) {
    platform = 'windows';
  } else if (/Macintosh|Mac OS X/.test(ua)) {
    platform = 'macos';
  } else if (/Linux/.test(ua) && !/Android/.test(ua)) {
    platform = 'linux';
  }
  
  var detection = document.getElementById('detection');
  var detected = '';
  switch(platform) {
    case 'windows': detected = 'Windows'; break;
    case 'linux': detected = 'Linux'; break;
    case 'ios': detected = 'iOS'; break;
    case 'android': detected = 'Android'; break;
    case 'macos': detected = 'macOS'; break;
    default: detected = '未识别'; break;
  }
  detection.innerHTML = '<strong>检测到系统：</strong>' + detected + '（UA: ' + ua.substring(0, 80) + (ua.length > 80 ? '...' : '') + '）';
  
  var el = document.getElementById(platform);
  if (el) {
    el.classList.add('active');
  } else {
    document.getElementById('unknown').classList.add('active');
  }
})();
</script>
</body>
</html>`
