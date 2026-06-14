// 本文件实现「把 deeproxy 安装为系统服务并启动」的完整流程（组件⑥）。
//
// 设计要点（对齐计划 step 22 与 AC-6.1..6.8）：
//   - 复用成熟库 github.com/kardianos/service，统一抽象 Linux systemd / Windows SCM；
//     macOS 明确不支持（打印提示并退出），不做静默降级。
//   - 服务名固定 "deeproxy"；重复运行 --daemon 时幂等改写同名服务，而非新增。
//   - WorkingDirectory 设为 exe 所在目录，保证服务运行时 ./deeproxy.db 仍落在 exe 旁。
//   - 启动参数 = exe 路径 + filterServiceArgs(os.Args[1:])（剔除 --daemon/--startup）。
//   - 开机自启：Windows 用 Option["StartType"]=automatic/manual；Linux 用原始
//     systemctl enable/disable（systemd 无对应 kardianos Option）。
//   - 安装成功但启动失败 → 立即 Uninstall 回滚，不留半注册服务。
//   - 权限不足 → 中文提示「需以 root / 管理员身份运行」，不自动提权。
//
// 注意：本包只负责「安装/启动管理」。服务真正运行时，systemd/SCM 会以过滤后的参数
// 重新拉起本二进制（此时不含 --daemon），main 走正常路径启动 SOCKS5 + Web 服务。
// 故 program.Start/Stop 仅为满足 kardianos Interface 的占位实现，无需在此真正起服务。
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
)

// serviceName 是固定的系统服务名（AC-6.2：重跑改写同名服务而非新增）。
const serviceName = "deeproxy"

// program 是 kardianos service.Interface 的占位实现。
// 真实业务由「服务被系统拉起后、以过滤参数重新执行本二进制」承担（见文件头注释），
// 故这里 Start/Stop 不做任何事，仅满足接口签名以便构造 Service 做安装管理。
type program struct{}

func (p *program) Start(s service.Service) error { return nil }
func (p *program) Stop(s service.Service) error  { return nil }

// fatal 打印中文错误并以退出码 1 结束进程（DRY：统一失败出口）。
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// resolveExe 返回当前可执行文件的真实绝对路径与其所在目录。
// 必须检查 os.Executable / EvalSymlinks 的错误（AC-6.3）：若吞掉错误得到空路径，
// 会导致 WorkingDirectory 为空、服务运行时 ./deeproxy.db 落到错误位置。
func resolveExe() (exe string, workDir string) {
	exe, err := os.Executable()
	if err != nil {
		fatal("无法获取可执行文件路径: %v", err)
	}
	// 解析符号链接，拿到真实路径（如通过软链调用时）。
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		fatal("解析可执行文件软链接失败: %v", err)
	}
	return exe, filepath.Dir(exe)
}

// isPermissionErr 粗粒度判断错误是否为权限不足类（用于给出友好的 root/管理员提示，AC-6.8）。
func isPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// 跨平台兜底：systemd / SCM 的权限错误文案各异，按关键字粗匹配。
	for _, kw := range []string{"permission denied", "access is denied", "must be root", "administrator", "not permitted"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// InstallAndStart 把 deeproxy 安装为系统服务并启动；startup 为 true 时设为开机自启。
// 整个流程在打开 DB / 绑定端口之前由 main 调用，完成后前台进程退出（AC-6.7）。
func InstallAndStart(startup bool) {
	// —— 平台分发（AC-6.1）——
	switch runtime.GOOS {
	case "darwin":
		// macOS 明确不支持系统服务（spec 决策），直接提示并退出。
		fatal("macOS 不支持系统服务，请改用 launchd 或前台运行 deeproxy")
	case "linux":
		// Linux 仅支持 systemd：systemctl 不存在即报错失败退出，不静默降级（R-3）。
		if _, err := exec.LookPath("systemctl"); err != nil {
			fatal("未检测到 systemctl，本工具仅支持 systemd 系统服务；请在 systemd 环境下运行")
		}
	case "windows":
		// Windows 走 SCM，由 kardianos 自动处理。
	default:
		fatal("当前平台 %s 不支持安装为系统服务", runtime.GOOS)
	}

	// —— 解析 exe 与工作目录（AC-6.3，错误不可吞）——
	exe, workDir := resolveExe()

	// —— 组装服务配置（AC-6.2/6.3/6.4/6.6）——
	cfg := &service.Config{
		Name:             serviceName,
		DisplayName:      "deeproxy",
		Description:      "deeproxy SOCKS5 中继转发工具 + Web 管理后台",
		Executable:       exe,
		Arguments:        filterServiceArgs(os.Args[1:]),
		WorkingDirectory: workDir,
		Option:           service.KeyValue{},
	}
	// 开机自启（仅 Windows 在 Install 前通过 StartType 表达；Linux 见下方 enable/disable）。
	if runtime.GOOS == "windows" {
		if startup {
			cfg.Option["StartType"] = "automatic" // 开机自动启动
		} else {
			cfg.Option["StartType"] = "manual" // 安装但不自启
		}
	}

	svc, err := service.New(&program{}, cfg)
	if err != nil {
		fatal("构造系统服务失败: %v", err)
	}

	// —— 幂等重装（AC-6.2）：若同名服务在运行，先停稳再卸载，再重新安装 ——
	reinstallIfPresent(svc)

	// —— 安装（带 StartType，AC-6.6 Windows 分支）——
	if err := svc.Install(); err != nil {
		if isPermissionErr(err) {
			fatal("安装系统服务失败：权限不足，请以 root / 管理员身份运行")
		}
		fatal("安装系统服务失败: %v", err)
	}

	// —— Linux 开机自启控制（AC-6.6）：systemd 无 kardianos StartType Option，
	//     用原始 systemctl enable/disable；systemctl 已由上方 LookPath 保证存在 ——
	if runtime.GOOS == "linux" {
		action := "disable"
		if startup {
			action = "enable"
		}
		if out, eerr := exec.Command("systemctl", action, serviceName).CombinedOutput(); eerr != nil {
			// enable/disable 失败不致命到留下半装服务，但需明确告知（多为权限问题）。
			fmt.Fprintf(os.Stderr, "设置开机自启(%s)失败: %v\n%s\n", action, eerr, string(out))
		}
	}

	// —— 启动服务；失败则回滚卸载（AC-6.5）——
	if err := svc.Start(); err != nil {
		// 启动失败：清理刚安装的半注册服务，避免残留。
		if uerr := svc.Uninstall(); uerr != nil {
			fmt.Fprintf(os.Stderr, "回滚卸载服务失败: %v\n", uerr)
		}
		if isPermissionErr(err) {
			fatal("启动系统服务失败：权限不足，请以 root / 管理员身份运行")
		}
		fatal("启动系统服务失败（已回滚卸载）: %v", err)
	}

	fmt.Printf("系统服务 %q 已安装并启动；开机自启=%v；工作目录=%s\n", serviceName, startup, workDir)
}

// reinstallIfPresent 实现幂等重装的「先停稳再卸载」前半段（AC-6.2）。
// 若服务存在且在运行：Stop → 轮询 Status 直到非 Running（带上限超时）→ Uninstall。
// 各步骤的「未安装」类错误一律忽略（首次安装时服务本就不存在）。
func reinstallIfPresent(svc service.Service) {
	status, err := svc.Status()
	if err != nil {
		// Status 失败通常意味着服务未安装（StatusUnknown），视为「无需重装」直接返回。
		return
	}
	if status == service.StatusUnknown {
		return
	}

	// 若在运行，先停止并轮询等待其真正停下，避免旧进程仍占端口导致新服务 Start 偶发 bind 失败。
	if status == service.StatusRunning {
		_ = svc.Stop() // 停止失败也继续尝试卸载（忽略错误）
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s, serr := svc.Status()
			if serr != nil || s != service.StatusRunning {
				break // 已非运行态（或无法查询，视为已停）
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// 卸载旧服务（忽略「未安装」类错误）。
	_ = svc.Uninstall()
}
