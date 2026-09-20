package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LauncherConfig 启动器配置
type LauncherConfig struct {
	GliderPath      string
	GliderConfig    string
	GliderWatch     bool
	PacwebPath      string
	PacwebConfig    string
	PacwebWatch     bool
	RestartDelay    int
	RestartMaxRetry int
}

// ManagedProcess 被管理的子进程
type ManagedProcess struct {
	Name       string
	ExePath    string
	Args       []string
	ConfigPath string
	Watch      bool
	cmd        *exec.Cmd
	mu         sync.Mutex
	restarts   int
	lastStart  time.Time
	stopChan   chan struct{}
}

var (
	cfg      LauncherConfig
	exeDir   string
	stopAll  chan struct{}
)

func main() {
	// 获取可执行文件目录
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("无法获取可执行文件路径: %v", err)
	}
	exeDir = filepath.Dir(exe)

	// 确定配置文件路径
	configFile := filepath.Join(exeDir, "launcher.conf")
	if len(os.Args) > 2 && os.Args[1] == "-config" {
		configFile = os.Args[2]
	}

	// 加载或创建默认配置
	cfg, err = loadOrCreateConfig(configFile)
	if err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	log.Printf("=== 集中启动器 ===")
	log.Printf("glider: %s (watch=%v)", cfg.GliderPath, cfg.GliderWatch)
	log.Printf("pacweb: %s (watch=%v)", cfg.PacwebPath, cfg.PacwebWatch)

	// 杀掉可能残留的旧进程
	killExisting(cfg.GliderPath)
	killExisting(cfg.PacwebPath)

	stopAll = make(chan struct{})

	// 创建被管理的进程
	gliderPath := resolvePath(cfg.GliderPath)
	pacwebPath := resolvePath(cfg.PacwebPath)

	gliderProc := &ManagedProcess{
		Name:       "glider",
		ExePath:    gliderPath,
		Args:       []string{"-config", resolvePath(cfg.GliderConfig)},
		ConfigPath: resolvePath(cfg.GliderConfig),
		Watch:      cfg.GliderWatch,
	}

	pacwebProc := &ManagedProcess{
		Name:       "pacweb",
		ExePath:    pacwebPath,
		Args:       []string{"-config", resolvePath(cfg.PacwebConfig)},
		ConfigPath: resolvePath(cfg.PacwebConfig),
		Watch:      cfg.PacwebWatch,
	}

	// 启动两个进程
	gliderProc.start()
	pacwebProc.start()

	// 启动健康监控（崩溃自动重启）
	go gliderProc.monitor()
	go pacwebProc.monitor()

	// 启动配置文件监控（仅对 Watch=true 的进程）
	if cfg.GliderWatch {
		go watchConfig(gliderProc)
	}

	// 等待退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("收到退出信号，正在停止所有子进程...")

	// 停止所有子进程
	close(stopAll)
	gliderProc.stop()
	pacwebProc.stop()

	log.Printf("所有子进程已停止，退出。")
}

// start 启动子进程
func (p *ManagedProcess) start() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查可执行文件是否存在
	if _, err := os.Stat(p.ExePath); os.IsNotExist(err) {
		log.Printf("[%s] 可执行文件不存在: %s", p.Name, p.ExePath)
		return
	}

	cmd := exec.Command(p.ExePath, p.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = exeDir

	if err := cmd.Start(); err != nil {
		log.Printf("[%s] 启动失败: %v", p.Name, err)
		return
	}

	p.cmd = cmd
	p.lastStart = time.Now()
	p.stopChan = make(chan struct{})
	log.Printf("[%s] 已启动 (PID=%d)", p.Name, cmd.Process.Pid)
}

// stop 停止子进程
func (p *ManagedProcess) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil || p.cmd.Process == nil {
		return
	}

	log.Printf("[%s] 正在停止 (PID=%d)...", p.Name, p.cmd.Process.Pid)

	// 尝试优雅停止
	p.cmd.Process.Signal(os.Interrupt)

	// 等待退出（最多 5 秒）
	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			log.Printf("[%s] 已退出: %v", p.Name, err)
		} else {
			log.Printf("[%s] 已正常退出", p.Name)
		}
	case <-time.After(5 * time.Second):
		log.Printf("[%s] 优雅停止超时，强制终止", p.Name)
		p.cmd.Process.Kill()
		<-done
	}

	close(p.stopChan)
}

// monitor 监控子进程健康状态，崩溃时自动重启
func (p *ManagedProcess) monitor() {
	for {
		select {
		case <-stopAll:
			return
		default:
		}

		if p.cmd == nil || p.cmd.Process == nil {
			time.Sleep(time.Duration(cfg.RestartDelay) * time.Second)
			p.start()
			continue
		}

		// 等待进程退出
		done := make(chan error, 1)
		go func() {
			if p.cmd != nil && p.cmd.Process != nil {
				done <- p.cmd.Wait()
			}
		}()

		select {
		case err := <-done:
			// 检查是否是主动停止
			select {
			case <-p.stopChan:
				return
			default:
			}

			if err != nil {
				log.Printf("[%s] 进程异常退出: %v", p.Name, err)
			} else {
				log.Printf("[%s] 进程已退出", p.Name)
			}

			// 重启退避逻辑
			p.restarts++
			if p.restarts > cfg.RestartMaxRetry {
				log.Printf("[%s] 已达到最大重启次数 %d，停止重试", p.Name, cfg.RestartMaxRetry)
				return
			}

			// 如果距离上次启动不到 10 秒就崩溃了，增加退避
			if time.Since(p.lastStart) < 10*time.Second {
				delay := time.Duration(cfg.RestartDelay*p.restarts) * time.Second
				log.Printf("[%s] 快速崩溃，等待 %v 后重试 (第 %d/%d 次)", p.Name, delay, p.restarts, cfg.RestartMaxRetry)
				time.Sleep(delay)
			} else {
				// 正常崩溃，重置重启计数
				p.restarts = 0
				log.Printf("[%s] %v 后重启...", p.Name, time.Duration(cfg.RestartDelay)*time.Second)
				time.Sleep(time.Duration(cfg.RestartDelay) * time.Second)
			}

			p.start()

		case <-stopAll:
			return
		}
	}
}

// watchConfig 监控配置文件变更，触发重启
func watchConfig(p *ManagedProcess) {
	var lastMod time.Time
	interval := 2 * time.Second

	for {
		time.Sleep(interval)

		select {
		case <-stopAll:
			return
		default:
		}

		info, err := os.Stat(p.ConfigPath)
		if err != nil {
			continue
		}

		if info.ModTime() != lastMod {
			if !lastMod.IsZero() {
				log.Printf("[%s] 检测到配置文件变更，正在重启...", p.Name)
				p.stop()

				// 重置 stopChan 和重启计数
				p.mu.Lock()
				p.stopChan = make(chan struct{})
				p.restarts = 0
				p.mu.Unlock()

				time.Sleep(1 * time.Second)
				p.start()
				log.Printf("[%s] 配置重载完成", p.Name)
			}
			lastMod = info.ModTime()
		}
	}
}

// killExisting 杀掉可能残留的旧进程（按可执行文件名）
func killExisting(exePath string) {
	name := filepath.Base(exePath)
	// 去掉 .exe 后缀
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSuffix(name, ".EXE")

	// 用 taskkill 杀掉同名进程
	cmd := exec.Command("taskkill", "/F", "/IM", name+".exe", "/T")
	cmd.Output() // 忽略错误（进程可能不存在）
}

// resolvePath 将相对路径解析为基于 exeDir 的绝对路径
func resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(exeDir, p)
}

// loadOrCreateConfig 加载或创建默认配置
func loadOrCreateConfig(path string) (LauncherConfig, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(defaultConfigContent()), 0644); err != nil {
			return LauncherConfig{}, fmt.Errorf("无法创建默认配置文件: %w", err)
		}
		log.Printf("已创建默认配置文件: %s", path)
	}
	return parseConfig(path), nil
}

func defaultConfigContent() string {
	return `# 集中启动器配置文件
# 自动生成，修改后需重启 launcher 生效

# glider 配置
glider_path = glider.exe
glider_config = glider.conf
glider_watch = true

# pacweb 配置
pacweb_path = pacweb.exe
pacweb_config = pacweb.conf
pacweb_watch = false

# 崩溃重启延迟（秒）
restart_delay = 3

# 最大重启次数（快速连续崩溃时）
restart_max_retry = 5
`
}

func parseConfig(path string) LauncherConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("读取配置文件失败: %v", err)
	}

	c := LauncherConfig{
		GliderPath:      "glider.exe",
		GliderConfig:    "glider.conf",
		GliderWatch:     true,
		PacwebPath:      "pacweb.exe",
		PacwebConfig:    "pacweb.conf",
		PacwebWatch:     false,
		RestartDelay:    3,
		RestartMaxRetry: 5,
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
		case "glider_path":
			c.GliderPath = val
		case "glider_config":
			c.GliderConfig = val
		case "glider_watch":
			c.GliderWatch = (val == "true" || val == "1" || val == "yes")
		case "pacweb_path":
			c.PacwebPath = val
		case "pacweb_config":
			c.PacwebConfig = val
		case "pacweb_watch":
			c.PacwebWatch = (val == "true" || val == "1" || val == "yes")
		case "restart_delay":
			fmt.Sscanf(val, "%d", &c.RestartDelay)
		case "restart_max_retry":
			fmt.Sscanf(val, "%d", &c.RestartMaxRetry)
		}
	}

	return c
}
