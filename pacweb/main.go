package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 配置结构
type Config struct {
	Listen          string
	CACertPath      string
	ProxyAddr       string
	DirectRanges    []string
	AutoExportCA    bool
	CASubjectFilter string
	CADisplayName   string
}

var (
	currentConfig atomic.Value   // 存储 *Config
	serverMu      sync.Mutex     // 保护 httpServer
	httpServer    *http.Server   // 当前 HTTP 服务实例
	httpMux       *http.ServeMux // 路由（不变，仅 listen 地址变）
	exeDir        string          // 可执行文件所在目录
	configFile    string          // 配置文件路径
)

func main() {
	// 获取可执行文件目录
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("无法获取可执行文件路径: %v", err)
	}
	exeDir = filepath.Dir(exe)

	// 确定配置文件路径（支持 -config 参数）
	configFile = filepath.Join(exeDir, "pacweb.conf")
	if len(os.Args) > 2 && os.Args[1] == "-config" {
		configFile = os.Args[2]
	}

	// 加载或创建默认配置
	cfg, err := loadOrCreateConfig(configFile)
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}
	currentConfig.Store(cfg)

	// 确保 Windows 防火墙规则存在（允许本程序入站）
	ensureFirewallRule()

	// 导出 CA 证书
	if cfg.AutoExportCA {
		certPath := filepath.Join(exeDir, cfg.CACertPath)
		if err := exportCA(cfg.CASubjectFilter, certPath); err != nil {
			log.Printf("[警告] CA 证书导出失败: %v", err)
		} else {
			log.Printf("CA 证书已导出到 %s", certPath)
		}
	}

	// 启动配置文件监控（热重载）
	go watchConfig(configFile, 2*time.Second)

	// 创建路由并注册 handler
	httpMux = http.NewServeMux()
	registerHandlers(httpMux)

	// 启动 HTTP 服务
	startServer(cfg)

	// 阻塞主协程
	select {}
}

// loadOrCreateConfig 加载配置文件，不存在则创建默认配置
func loadOrCreateConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(defaultConfigContent()), 0644); err != nil {
			return nil, fmt.Errorf("无法创建默认配置文件: %w", err)
		}
		log.Printf("已创建默认配置文件: %s", path)
	}
	return parseConfig(path)
}

// defaultConfigContent 返回默认配置文件内容
func defaultConfigContent() string {
	return `# pacweb 配置文件
# 自动生成，修改保存后自动热重载（无需重启）

# 监听地址
listen = :8088

# CA 证书导出路径
ca_cert = ca.crt

# glider 代理地址（PAC 文件中引用）
proxy_addr = 192.168.155.22:7778

# 内网直连网段（逗号分隔，PAC 文件中引用）
direct_ranges = 192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,127.0.0.0/8

# 是否自动从 Windows 证书库导出 CA
auto_export_ca = true

# 证书库中查找的关键字（CA 的 Subject 中包含此关键字，用于定位待导出的根证书）
ca_subject_filter = AdGuard

# CA 显示名称（用于引导页、iOS 描述文件等用户可见界面）
# 如果使用其他工具（如 mitmproxy、Caddy 等），请改为对应名称
ca_display_name = AdGuard CA
`
}

// parseConfig 解析 key=value 格式的配置文件
func parseConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Listen:          ":8088",
		CACertPath:      "ca.crt",
		ProxyAddr:       "192.168.155.22:7778",
		DirectRanges:    []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "127.0.0.0/8"},
		AutoExportCA:    true,
		CASubjectFilter: "AdGuard",
		CADisplayName:   "AdGuard CA",
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "listen":
			cfg.Listen = val
		case "ca_cert":
			cfg.CACertPath = val
		case "proxy_addr":
			cfg.ProxyAddr = val
		case "direct_ranges":
			if val != "" {
				ranges := strings.Split(val, ",")
				cfg.DirectRanges = make([]string, 0, len(ranges))
				for _, r := range ranges {
					if r = strings.TrimSpace(r); r != "" {
						cfg.DirectRanges = append(cfg.DirectRanges, r)
					}
				}
			}
		case "auto_export_ca":
			cfg.AutoExportCA = (val == "true" || val == "1" || val == "yes")
		case "ca_subject_filter":
			cfg.CASubjectFilter = val
		case "ca_display_name":
			cfg.CADisplayName = val
		}
	}

	return cfg, nil
}

// watchConfig 轮询监控配置文件变更，触发热重载
func watchConfig(path string, interval time.Duration) {
	var lastMod time.Time
	for {
		time.Sleep(interval)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime() != lastMod {
			if !lastMod.IsZero() {
				log.Printf("检测到配置文件变更，正在热重载...")
				cfg, err := parseConfig(path)
				if err != nil {
					log.Printf("[错误] 配置重载失败: %v", err)
					continue
				}
				old := currentConfig.Load().(*Config)
				currentConfig.Store(cfg)

				// CA 相关配置变更时重新导出
				if cfg.AutoExportCA && (old.CASubjectFilter != cfg.CASubjectFilter || old.CACertPath != cfg.CACertPath) {
					certPath := filepath.Join(exeDir, cfg.CACertPath)
					if err := exportCA(cfg.CASubjectFilter, certPath); err != nil {
						log.Printf("[警告] CA 重新导出失败: %v", err)
					} else {
						log.Printf("CA 证书已重新导出")
					}
				}

				// 监听地址变更时重启 HTTP 服务
				if old.Listen != cfg.Listen {
					log.Printf("监听地址变更 %s → %s，重启 HTTP 服务", old.Listen, cfg.Listen)
					restartServer(cfg)
				}

				log.Printf("配置热重载完成")
			}
			lastMod = info.ModTime()
		}
	}
}

// startServer 启动 HTTP 服务
func startServer(cfg *Config) {
	serverMu.Lock()
	defer serverMu.Unlock()

	httpServer = &http.Server{
		Addr:    cfg.Listen,
		Handler: httpMux,
	}

	log.Printf("pacweb 启动于 %s", cfg.Listen)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if isAddrInUseError(err) {
				owner := findPortOwner(cfg.Listen)
				if owner != "" {
					log.Fatalf("[错误] 端口 %s 被占用 (%s)，请停止该进程或修改配置中的 listen 值后重试", cfg.Listen, owner)
				}
				log.Fatalf("[错误] 端口 %s 被占用，请修改配置中的 listen 值后重试", cfg.Listen)
			}
			log.Fatalf("[错误] HTTP 服务失败: %v", err)
		}
	}()
}

// restartServer 重启 HTTP 服务（监听地址变更时调用）
func restartServer(cfg *Config) {
	serverMu.Lock()
	defer serverMu.Unlock()

	if httpServer != nil {
		httpServer.Close()
	}

	httpServer = &http.Server{
		Addr:    cfg.Listen,
		Handler: httpMux,
	}

	log.Printf("pacweb 重启于 %s", cfg.Listen)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if isAddrInUseError(err) {
				owner := findPortOwner(cfg.Listen)
				if owner != "" {
					log.Printf("[错误] 端口 %s 被占用 (%s)，请停止该进程或修改配置中的 listen 值后重试", cfg.Listen, owner)
					return
				}
				log.Printf("[错误] 端口 %s 被占用，请修改配置中的 listen 值后重试", cfg.Listen)
				return
			}
			log.Printf("[错误] HTTP 服务重启失败: %v", err)
		}
	}()
}

// getConfig 从 atomic.Value 中获取当前配置
func getConfig() *Config {
	return currentConfig.Load().(*Config)
}
