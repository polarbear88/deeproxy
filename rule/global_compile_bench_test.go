package rule

import (
	"fmt"
	"testing"

	"deeproxy/config"
)

// global_compile_bench_test.go 量化 D① 优化收益：模拟「多分组共享大量全局规则」场景，
// 对比「每组整体编译全局段（旧）」与「全局编译一次复用（新）」的编译开销。
//
// 跑法：go test ./rule/ -bench BenchmarkGroupEngineBuild -benchmem

// makeGlobalSpecs 造 n 条全局规则（一半 ip-cidr 走 ParseCIDR、一半 domain-suffix 走 canonicalize，
// 即编译热点）。
func makeGlobalSpecs(n int) []config.RuleSpec {
	out := make([]config.RuleSpec, 0, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			out = append(out, spec(fmt.Sprintf("ip-cidr:10.%d.%d.0/24", i%256, (i/256)%256), "direct"))
		} else {
			out = append(out, spec(fmt.Sprintf("domain-suffix:host%d.example.com", i), "reject"))
		}
	}
	return out
}

// benchBuildAllGroups 模拟一次快照重建里「为 G 个分组各构建一个引擎」的总编译开销。
// global 已编译产物在新路径下只算一次；旧路径每组都重编全局段。
func benchBuildAllGroups(b *testing.B, groups int, globalSpecs []config.RuleSpec, reuseGlobal bool) {
	groupSpecs := []config.RuleSpec{spec("domain:per-group.com", "forward")}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if reuseGlobal {
			// 新路径：全局编译一次，G 个分组复用。
			compiled, err := CompileRules(globalSpecs)
			if err != nil {
				b.Fatal(err)
			}
			for g := 0; g < groups; g++ {
				if _, err := NewEngineWithGlobal(compiled, groupSpecs, ActionForward); err != nil {
					b.Fatal(err)
				}
			}
		} else {
			// 旧路径：每个分组都整体编译「全局段 + 分组段」。
			for g := 0; g < groups; g++ {
				if _, err := BuildGroupEngine(
					[]RuleGroupSpec{{Name: "g", Specs: globalSpecs}},
					[]RuleGroupSpec{{Name: "p", Specs: groupSpecs}},
					ActionForward,
				); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}

// BenchmarkGroupEngineBuild_Old_50g_200r：旧路径，50 组 × 200 条全局规则。
func BenchmarkGroupEngineBuild_Old_50g_200r(b *testing.B) {
	benchBuildAllGroups(b, 50, makeGlobalSpecs(200), false)
}

// BenchmarkGroupEngineBuild_New_50g_200r：新路径（全局编译一次复用），同规模。
func BenchmarkGroupEngineBuild_New_50g_200r(b *testing.B) {
	benchBuildAllGroups(b, 50, makeGlobalSpecs(200), true)
}
