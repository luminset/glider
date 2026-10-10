package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
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

// ManagedProcess 被管理的子进程。
// 生命周期由 run goroutine 独占：只有 run 会调用 cmd.Wait，
// stop/restart 只能通过 desired/stopping 传递意图，避免并发 Wait 和重复子进程。
type ManagedProcess struct {
	Name       string
	ExePath    string
	Args       []string
	ConfigPath string
	Watch      bool

	mu        sync.Mutex
	cmd       *exec.Cmd
	desired   string        // 期望的下一步动作
	stopping  bool          // true 时 run 不再启动新一代子进程
	exited    chan struct{} // run 退出时关闭
	restarts  int
	lastStart time.Time
}

// run 循环内识别的动作
const (
	desiredNone    = ""
	desiredStop    = "stop"
	desiredRestart = "restart"
)

var (
	cfg     LauncherConfig
	exeDir  string
	stopAll chan struct{}
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

	// 创建被管理的进程
	gliderPath := resolvePath(cfg.GliderPath)
	pacwebPath := resolvePath(cfg.PacwebPath)

	// 杀掉可能残留的旧进程
	stopAll = make(chan struct{})
	killExisting(gliderPath)
	killExisting(pacwebPath)

	gliderProc := &ManagedProcess{
		Name:       "glider",
		ExePath:    gliderPath,
		Args:       []string{"-config", resolvePath(cfg.GliderConfig)},
		ConfigPath: resolvePath(cfg.GliderConfig),
		Watch:      cfg.GliderWatch,
		exited:     make(chan struct{}),
	}

	pacwebProc := &ManagedProcess{
		Name:       "pacweb",
		ExePath:    pacwebPath,
		Args:       []string{"-config", resolvePath(cfg.PacwebConfig)},
		ConfigPath: resolvePath(cfg.PacwebConfig),
		Watch:      cfg.PacwebWatch,
		exited:     make(chan struct{}),
	}

	// 启动健康监控（崩溃自动重启），run 自身负责首次拉起子进程
	go gliderProc.run()
	go pacwebProc.run()

	// 启动配置文件监控（仅对 Watch=true 的进程）
	if cfg.GliderWatch {
		go watchConfig(gliderProc)
	}
	if cfg.PacwebWatch {
		go watchConfig(pacwebProc)
	}

	// 等待退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("收到退出信号，正在停止所有子进程...")

	// 停止所有子进程
	close(stopAll)
	gliderProc.shutdown()
	pacwebProc.shutdown()

	log.Printf("所有子进程已停止，退出。")
}

// spawn 拉起一代子进程并登记到 p.cmd
func (p *ManagedProcess) spawn() (*exec.Cmd, error) {
	if _, err := os.Stat(p.ExePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("可执行文件不存在: %s", p.ExePath)
	}

	cmd := exec.Command(p.ExePath, p.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = exeDir

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.cmd = cmd
	p.lastStart = time.Now()
	p.mu.Unlock()

	log.Printf("[%s] 已启动 (PID=%d)", p.Name, cmd.Process.Pid)
	return cmd, nil
}

// run 是子进程生命周期的唯一拥有者：启动、等待退出、按请求或崩溃决定是否重启
func (p *ManagedProcess) run() {
	defer close(p.exited)

	for {
		if p.shouldStop() {
			return
		}

		cmd, err := p.spawn()
		if err != nil {
			log.Printf("[%s] 启动失败: %v", p.Name, err)
			if !sleepOrStop(restartDelay()) {
				return
			}
			continue
		}

		// 停止请求可能恰好在 spawn 之后到达，此时没人再给它发信号
		if p.stoppingRequested() {
			terminate(cmd.Process)
		}

		waitErr := cmd.Wait()

		p.mu.Lock()
		p.cmd = nil
		desired := p.desired
		p.desired = desiredNone
		p.mu.Unlock()

		switch desired {
		case desiredStop:
			log.Printf("[%s] 已按请求停止", p.Name)
			return
		case desiredRestart:
			p.mu.Lock()
			p.restarts = 0
			p.mu.Unlock()
			log.Printf("[%s] 已按请求重启", p.Name)
			continue
		}

		if p.shouldStop() {
			return
		}

		if waitErr != nil {
			log.Printf("[%s] 进程异常退出: %v", p.Name, waitErr)
		} else {
			log.Printf("[%s] 进程已退出", p.Name)
		}

		p.mu.Lock()
		p.restarts++
		restarts := p.restarts
		quickCrash := time.Since(p.lastStart) < 10*time.Second
		if !quickCrash {
			p.restarts = 0
		}
		p.mu.Unlock()

		if restarts > cfg.RestartMaxRetry {
			log.Printf("[%s] 已达到最大重启次数 %d，停止重试", p.Name, cfg.RestartMaxRetry)
			return
		}

		delay := restartDelay()
		if quickCrash {
			delay = time.Duration(cfg.RestartDelay*restarts) * time.Second
			log.Printf("[%s] 快速崩溃，等待 %v 后重试 (第 %d/%d 次)", p.Name, delay, restarts, cfg.RestartMaxRetry)
		} else {
			log.Printf("[%s] %v 后重启...", p.Name, delay)
		}

		if !sleepOrStop(delay) {
			return
		}
	}
}

// restart 请求重启当前子进程（用于配置热重载）
func (p *ManagedProcess) restart() {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	p.desired = desiredRestart
	cmd := p.cmd
	p.mu.Unlock()

	if cmd != nil {
		terminate(cmd.Process)
	}
}

// shutdown 请求停止，并等待 run 回收子进程
func (p *ManagedProcess) shutdown() {
	p.mu.Lock()
	p.stopping = true
	p.desired = desiredStop
	cmd := p.cmd
	p.mu.Unlock()

	if cmd != nil {
		terminate(cmd.Process)
	}

	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		log.Printf("[%s] 停止超时", p.Name)
	}
}

func (p *ManagedProcess) shouldStop() bool {
	select {
	case <-stopAll:
		return true
	default:
	}
	return p.stoppingRequested()
}

func (p *ManagedProcess) stoppingRequested() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopping
}

// terminate 停止子进程。Windows 下 os.Interrupt 不被支持，
// 需要连同子进程树一起终止，否则孙进程会残留并继续占用端口/输出管道。
func terminate(proc *os.Process) {
	if proc == nil {
		return
	}
	if err := proc.Signal(os.Interrupt); err == nil {
		return
	}

	kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(proc.Pid))
	kill.Stdout, kill.Stderr = os.Stdout, os.Stderr
	if err := kill.Run(); err != nil {
		proc.Kill()
	}
}

// sleepOrStop 等待 d，期间收到全局停止则返回 false
func sleepOrStop(d time.Duration) bool {
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-stopAll:
		return false
	}
}

// restartDelay 重启延迟，屏蔽非法配置值
func restartDelay() time.Duration {
	if cfg.RestartDelay < 1 {
		return 3 * time.Second
	}
	return time.Duration(cfg.RestartDelay) * time.Second
}

// watchConfig 监控配置文件变更，触发重启
func watchConfig(p *ManagedProcess) {
	var lastMod time.Time

	for {
		if !sleepOrStop(2 * time.Second) {
			return
		}

		info, err := os.Stat(p.ConfigPath)
		if err != nil {
			continue
		}

		if info.ModTime() == lastMod {
			continue
		}
		first := lastMod.IsZero()
		lastMod = info.ModTime()
		if first {
			continue
		}

		log.Printf("[%s] 检测到配置文件变更，正在重启...", p.Name)
		p.restart()
	}
}

// killExisting 杀掉可能残留的旧进程（按可执行文件名）
func killExisting(exePath string) {
	// 路径不存在时不做全局杀进程，避免按错误配置名误杀无关进程
	if _, err := os.Stat(exePath); err != nil {
		return
	}

	name := filepath.Base(exePath)
	// 去掉 .exe 后缀
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSuffix(name, ".EXE")
	if name == "" || name == "." || name == ".." {
		return
	}

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

	// 空值会让 resolvePath 退回到目录本身，保留默认更可用
	if c.GliderPath == "" {
		c.GliderPath = "glider.exe"
	}
	if c.PacwebPath == "" {
		c.PacwebPath = "pacweb.exe"
	}
	if c.RestartMaxRetry < 0 {
		c.RestartMaxRetry = 5
	}

	return c
}
