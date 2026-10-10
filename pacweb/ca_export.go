package main

import (
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// caFilterEnv 用于把匹配关键字交给 PowerShell：脚本本身是固定文本，
// 配置内容不进命令行，既不会被当作正则/通配符，也不可能注入额外语句。
const caFilterEnv = "GLIDER_PACWEB_CA_FILTER"

const caExportPSScript = `
$f = $env:GLIDER_PACWEB_CA_FILTER
$c = Get-ChildItem Cert:\LocalMachine\Root, Cert:\CurrentUser\Root |
    Where-Object { $_.Subject -and $_.Subject.ToLower().Contains($f.ToLower()) } |
    Select-Object -First 1
if ($c) {
    [Convert]::ToBase64String($c.RawData, [Base64FormattingOptions]::InsertLineBreaks)
} else {
    Write-Output 'NOT_FOUND'
}
`

// caExportCommand 组装 PowerShell 调用：脚本走 -EncodedCommand，
// 匹配关键字只通过环境变量传入，不作为任何命令行参数出现。
func caExportCommand(subjectFilter string) *exec.Cmd {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePSCommand(caExportPSScript))
	cmd.Env = append(os.Environ(), caFilterEnv+"="+subjectFilter)
	return cmd
}

// exportCA 从 Windows 证书库导出 CA 证书为 PEM 格式（通过 Subject 子串匹配，忽略大小写）
func exportCA(subjectFilter, outputPath string) error {
	if strings.TrimSpace(subjectFilter) == "" {
		return fmt.Errorf("ca_subject_filter 为空，无法定位要导出的根证书")
	}

	// 确保 目录存在
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	output, err := caExportCommand(subjectFilter).Output()
	if err != nil {
		// PowerShell 不可用时尝试 certutil 方式
		return exportCAViaCertutil(subjectFilter, outputPath)
	}

	result := strings.TrimSpace(string(output))
	if result == "NOT_FOUND" || result == "" {
		return fmt.Errorf("证书库中未找到包含 %q 的根证书", subjectFilter)
	}

	der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(result), ""))
	if err != nil {
		return fmt.Errorf("PowerShell 返回的证书数据无法解码: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("导出的证书格式无效: %w", err)
	}

	if err := os.WriteFile(outputPath, []byte(toPEM(der)), 0644); err != nil {
		return fmt.Errorf("写入证书文件失败: %w", err)
	}

	log.Printf("已导出根证书 %q", cert.Subject.CommonName)
	return nil
}

// toPEM 把 DER 编码为 PEM（64 字符换行）
func toPEM(der []byte) string {
	encoded := base64.StdEncoding.EncodeToString(der)
	var b strings.Builder
	b.WriteString("-----BEGIN CERTIFICATE-----\n")
	for len(encoded) > 64 {
		b.WriteString(encoded[:64])
		b.WriteByte('\n')
		encoded = encoded[64:]
	}
	b.WriteString(encoded)
	b.WriteString("\n-----END CERTIFICATE-----\n")
	return b.String()
}

// encodePSCommand 按 PowerShell 约定编码脚本（UTF-16LE 后 base64），
// 避免脚本中的换行与引号在经过 shell 解析时被改写。
func encodePSCommand(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, u := range units {
		data[2*i] = byte(u)
		data[2*i+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// exportCAViaCertutil 使用 certutil 作为后备方案导出证书
func exportCAViaCertutil(subjectFilter, outputPath string) error {
	// 列出 Root store 中的所有证书
	cmd := exec.Command("certutil", "-store", "Root")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("certutil 列出证书库失败: %w", err)
	}

	hash := findCertHash(string(output), subjectFilter)
	if hash == "" {
		return fmt.Errorf("certutil 未找到包含 %q 的证书", subjectFilter)
	}

	// 导出证书为 DER 格式
	derPath := outputPath + ".der"
	cmd = exec.Command("certutil", "-exportcert", "-store", "Root", hash, derPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("certutil 导出证书失败: %w", err)
	}

	// 读取 DER 并转为 PEM
	derData, err := os.ReadFile(derPath)
	if err != nil {
		return fmt.Errorf("读取 DER 文件失败: %w", err)
	}
	os.Remove(derPath)

	cert, err := x509.ParseCertificate(derData)
	if err != nil {
		return fmt.Errorf("certutil 导出的证书格式无效: %w", err)
	}

	if err := os.WriteFile(outputPath, []byte(toPEM(derData)), 0644); err != nil {
		return fmt.Errorf("写入 PEM 文件失败: %w", err)
	}

	log.Printf("通过 certutil 成功导出根证书 %q", cert.Subject.CommonName)
	return nil
}

// findCertHash 从 `certutil -store Root` 的输出里取匹配证书的 sha1 指纹。
// 按 ==== 分节逐张证书解析：字段名可能被本地化，而 "(sha1)" 与十六进制值是稳定的；
// 早期实现从 Subject 行向上搜索 "Cert Hash"，会取到上一张证书的指纹。
func findCertHash(storeOutput, subjectFilter string) string {
	want := strings.ToLower(subjectFilter)
	for _, section := range strings.Split(storeOutput, "================") {
		if !strings.Contains(strings.ToLower(section), want) {
			continue
		}
		idx := strings.Index(section, "(sha1)")
		if idx < 0 {
			continue
		}
		rest := section[idx+len("(sha1)"):]
		colon := strings.Index(rest, ":")
		if colon < 0 {
			continue
		}
		line, _, _ := strings.Cut(rest[colon+1:], "\n")
		hash := strings.ToUpper(strings.Join(strings.Fields(line), ""))
		if len(hash) == 40 && strings.Trim(hash, "0123456789ABCDEF") == "" {
			return hash
		}
	}
	return ""
}
