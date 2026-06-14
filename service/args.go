// 本文件实现服务安装时对启动参数的过滤（组件⑥，AC-6.4）。
//
// 背景：用户运行 `./deeproxy --socks5 1000 --web 1001 --daemon [--startup]` 安装系统服务时，
// 我们要把「当前进程的参数」原样登记为服务的 ExecStart 参数，但必须剔除 --daemon / --startup
// 这两个「仅用于触发安装动作」的开关——否则服务自身启动时又会触发一次安装（死循环）。
package service

// filterServiceArgs 从原始参数 args（通常是 os.Args[1:]）中剔除 daemon/startup 相关开关，
// 其余每个 token 原样按位置透传，返回过滤后的参数切片。
//
// 契约（AC-6.4，刻意保持简单且可预测）：
//   - 仅移除恰好等于以下 6 个字面量之一的 token：
//     --daemon、-daemon、--startup、-startup、--daemon=true、--startup=true。
//   - 其余 token 全部原样保留，且保持原有先后顺序（位置透传）。
//   - 【绝不】做以下任何处理：
//     · 不按 '=' 拆分 token（如 `--socks5=1000` 整段保留，不拆成 `--socks5` 与 `1000`）；
//     · 不剥离前导 '-' / '--'；
//     · 不做「值配对」——即不会因为看到 `--socks5` 就去吞掉它后面的 `1000`。
//     因此 `--socks5 1000` 是两个独立 token，二者都会被原样保留。
//
// 为什么不处理 `--daemon=false` 之类：进程能走到安装分支，前提是 flag 解析后 *daemon==true，
// 即命令行里出现的形式只能是 `--daemon` / `-daemon` / `--daemon=true`（`=false` 不会进入此分支），
// 故只需移除这 6 个形式即可覆盖全部「真值」写法。
//
// 注：stdlib `flag` 不支持捆绑短选项（如 `-dv` 同时表示 -d 与 -v），故不存在 `-dv` 这类
// 需要拆解的 token——无需也不应在此做拆分，args_test.go 中对此有说明。
func filterServiceArgs(args []string) []string {
	// 待移除的精确 token 集合（仅这 6 个字面量；其余一律透传）。
	drop := map[string]bool{
		"--daemon":       true,
		"-daemon":        true,
		"--startup":      true,
		"-startup":       true,
		"--daemon=true":  true,
		"--startup=true": true,
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if drop[a] {
			continue // 命中精确开关 → 丢弃，不影响其它 token 的位置
		}
		out = append(out, a) // 其余 token 原样按位置透传
	}
	return out
}
