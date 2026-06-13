package auth

import "strings"

// ParseVariables 解析 Type B 用户名尾段的「命名变量串」为 {name: value} 映射。
//
// 格式（spec「命名变量系统」权威约定）：
//
//	name1_value1#name2_value2#...
//	  · '#' 分隔多个变量；
//	  · '_' 分隔变量名与值（仅切第一个 '_'，故值里可再含 '_'）。
//
// 设计要点（解释为什么）：
//   - 值里可能含 '_'（如 session_abc_def 的值 "abc_def"），故对每段用 strings.Cut
//     只切【第一个】 '_'，把首个 '_' 之后的全部内容作为值，避免值被误切。
//   - 顺序无关：返回 map，调用方按名取用，不依赖书写顺序。
//   - 空尾段（""）合法：返回空 map（无变量），用于「Type B 无尾段/空尾段」场景。
//   - 容错：某段无 '_'（无法切出名值对）则跳过该段；变量名为空也跳过。
//     这是为了「多余/畸形输入忽略」的宽松语义，绝不因尾段格式问题拒连——
//     拒连只发生在鉴权失败，变量缺失走「缺值补空」而非报错。
//   - 重名：后出现的覆盖先出现的（map 自然行为），无歧义。
func ParseVariables(tail string) map[string]string {
	vars := make(map[string]string)
	if tail == "" {
		return vars
	}

	// 按 '#' 拆出每个「变量定义段」。
	for _, seg := range strings.Split(tail, "#") {
		if seg == "" {
			continue // 连续 '#' 或首尾 '#' 产生的空段，忽略。
		}
		// 只切第一个 '_'：左为变量名，右为值（值可再含 '_'）。
		name, value, ok := strings.Cut(seg, "_")
		if !ok || name == "" {
			continue // 无 '_' 或变量名为空，视为畸形段，忽略。
		}
		vars[name] = value
	}
	return vars
}

// SubstituteTemplate 把模板中的 {name} 占位按变量映射替换，返回替换后的字符串。
//
// 替换语义（spec「命名变量（Type B 上游用户名模板）」权威约定）：
//   - 隐式定义：模板里写了哪些 {xxx} 就有哪些变量，无需额外注册——本函数只认模板里
//     出现的占位。
//   - 缺值补空：模板有 {name} 但 vars 里没有该 name → 替换为空字符串。
//   - 多余忽略：vars 里有模板未使用的变量 → 自然不参与替换（本函数不遍历 vars）。
//   - 顺序无关：按占位在模板中出现的位置逐个替换，与 vars 的内部顺序无关。
//
// 实现说明（解释为什么自己扫描而非用正则）：
//   - 占位语法极简（仅 {标识符}），手写单趟扫描比引入 regexp 更轻、零额外依赖，
//     且能精确控制「未闭合 '{'」「空占位 {}」等边界（原样保留，不误吞模板字符）。
//   - 单趟扫描 O(n)，不在字节中继热路径（仅建连阶段每连接一次），开销可忽略。
func SubstituteTemplate(template string, vars map[string]string) string {
	// 无 '{' 直接返回，省去扫描（绝大多数固定用户名走此快路径）。
	if !strings.Contains(template, "{") {
		return template
	}

	var b strings.Builder
	b.Grow(len(template))

	for i := 0; i < len(template); {
		c := template[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		// 遇到 '{'，向后找匹配的 '}'。
		end := strings.IndexByte(template[i+1:], '}')
		if end < 0 {
			// 未闭合的 '{'：原样输出剩余内容，结束。
			b.WriteString(template[i:])
			break
		}
		name := template[i+1 : i+1+end]
		if name == "" {
			// 空占位 "{}"：不视为变量占位，原样保留这两个字符。
			b.WriteString("{}")
		} else {
			// vars 缺该 name 时取零值 ""（缺值补空）。
			b.WriteString(vars[name])
		}
		i += end + 2 // 跳过 "{name}" 整体（含两个花括号）。
	}
	return b.String()
}
