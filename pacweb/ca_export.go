package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// exportCA 从 Windows 证书库导出 CA 证书为 PEM 格式（通过 Subject 关键字匹配）
func exportCA(subjectFilter, outputPath string) error {
	// 确保 目录存在
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	// 用 PowerShell 查找证书并输出 base64 编码的原始数据
	psScript := fmt.Sprintf(`$cert = Get-ChildItem Cert:\LocalMachine\Root | Where-Object { $_.Subject -match "%s" } | Select-Object -First 1
if ($cert) {
    [Convert]::ToBase64String($cert.RawData, [Base64FormattingOptions]::InsertLineBreaks)
} elseif (Get-ChildItem Cert:\CurrentUser\Root | Where-Object { $_.Subject -match "%s" } | Select-Object -First 1) {
    $cert = Get-ChildItem Cert:\CurrentUser\Root | Where-Object { $_.Subject -match "%s" } | Select-Object -First 1
    [Convert]::ToBase64String($cert.RawData, [Base64FormattingOptions]::InsertLineBreaks)
} else {
    Write-Output "NOT_FOUND"
}`, subjectFilter, subjectFilter, subjectFilter)

	cmd := exec.Command("powershell", "-NoProfile", "-Command", psScript)
	output, err := cmd.Output()
	if err != nil {
		// PowerShell 不可用时尝试 certutil 方式
		return exportCAViaCertutil(subjectFilter, outputPath)
	}

	result := strings.TrimSpace(string(output))
	if result == "NOT_FOUND" || result == "" {
		return fmt.Errorf("证书库中未找到包含 %q 的根证书", subjectFilter)
	}

	// 构造 PEM 格式
	pem := "-----BEGIN CERTIFICATE-----\n" + result + "\n-----END CERTIFICATE-----\n"

	if err := os.WriteFile(outputPath, []byte(pem), 0644); err != nil {
		return fmt.Errorf("写入证书文件失败: %w", err)
	}

	return nil
}

// exportCAViaCertutil 使用 certutil 作为后备方案导出证书
func exportCAViaCertutil(subjectFilter, outputPath string) error {
	// 列出 Root store 中的所有证书
	cmd := exec.Command("certutil", "-store", "Root")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("certutil 列出证书库失败: %w", err)
	}

	// 解析输出，找到匹配的证书序列号（sha1 hash）
	lines := strings.Split(string(output), "\n")
	var hash string
	for i, line := range lines {
		if strings.Contains(line, subjectFilter) {
			// 向上搜索找到 Cert Hash 行
			for j := i; j >= 0 && j > i-10; j-- {
				if strings.Contains(lines[j], "Cert Hash") {
					// 提取 hash 值（去掉前缀和空格）
					parts := strings.SplitN(lines[j], ":", 2)
					if len(parts) == 2 {
						hash = strings.TrimSpace(parts[1])
						hash = strings.ReplaceAll(hash, " ", "")
						break
					}
				}
			}
			if hash != "" {
				break
			}
		}
	}

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

	pem := "-----BEGIN CERTIFICATE-----\n" +
		base64.StdEncoding.EncodeToString(derData) +
		"\n-----END CERTIFICATE-----\n"

	if err := os.WriteFile(outputPath, []byte(pem), 0644); err != nil {
		return fmt.Errorf("写入 PEM 文件失败: %w", err)
	}

	log.Printf("通过 certutil 成功导出证书")
	return nil
}
