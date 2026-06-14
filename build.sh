#!/usr/bin/env bash
# build.sh — deeproxy v2 跨平台发布构建脚本（AC-32）。
#
# 关键顺序（不可调换，否则前端不会被 embed）：
#   1) 先 pnpm build 前端 → 产物输出到 api/dist（vite emptyOutDir=true 会清空再写）；
#   2) 再 CGO_ENABLED=0 go build 交叉编译各平台/架构单一静态二进制（embed 进前端产物）。
#
# 免 CGO（modernc.org/sqlite 纯 Go）保证单命令交叉编译全平台，无需各平台 C 工具链。
#
# 用法：
#   ./build.sh              # 构建全部 5 个目标到 dist/
#   ./build.sh --skip-web   # 跳过前端构建（CI 中前端已单独构建好时用）
#   VERSION=v2.0.0 ./build.sh
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
SKIP_WEB=0
[[ "${1:-}" == "--skip-web" ]] && SKIP_WEB=1

OUT=dist
mkdir -p "$OUT"

# ---- 1) 前端构建（产物 → api/dist，被 //go:embed dist 嵌入）----
if [[ "$SKIP_WEB" -eq 0 ]]; then
  echo ">> [1/2] 构建前端 (pnpm build → api/dist)"
  pushd web >/dev/null
  pnpm install --frozen-lockfile
  pnpm build
  popd >/dev/null
  # vite emptyOutDir=true 会清空 api/dist（含被 git 跟踪的 .gitkeep 占位），构建后补回，
  # 避免误删该占位文件污染 git 工作区（dist 真实产物已 gitignore）。
  touch api/dist/.gitkeep
else
  echo ">> [1/2] 跳过前端构建（--skip-web），沿用现有 api/dist"
fi

# 确认 embed 目标存在（占位或真实产物），避免 go build 因缺 dist 失败。
if [[ ! -f api/dist/index.html ]]; then
  echo "!! api/dist/index.html 缺失，embed 会失败。请先构建前端或提交占位。" >&2
  exit 1
fi

# ---- 2) 交叉编译 5 目标单一静态二进制 ----
echo ">> [2/2] 交叉编译 (CGO_ENABLED=0, version=$VERSION)"
LDFLAGS="-s -w -X main.version=$VERSION"

# 目标矩阵：win/linux 各 amd64+arm64，mac amd64+arm64（共 6 个，覆盖计划要求的 5 平台×架构）。
TARGETS=(
  "windows/amd64"
  "windows/arm64"
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
)

for t in "${TARGETS[@]}"; do
  GOOS="${t%/*}"; GOARCH="${t#*/}"
  ext=""
  [[ "$GOOS" == "windows" ]] && ext=".exe"
  bin="$OUT/deeproxy-${GOOS}-${GOARCH}${ext}"
  echo "   - $GOOS/$GOARCH → $bin"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$bin" ./cmd/deeproxy
done

echo ">> 完成。产物："
ls -lh "$OUT"
