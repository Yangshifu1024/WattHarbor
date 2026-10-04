#!/usr/bin/env python3
"""守住「集成测试会被真正编译」这条。

## 要防的是什么

真库集成测试用 `//go:build integration` 隔离，好处是 `make check` 不依赖
Docker。代价是：文件一旦写错 tag、或者干脆忘了加 tag，
`go test ./...` 永远看不到它——它在仓库里躺着，看起来有测试，
实际上一次都没编译过。这类"护栏靠放行通过"是最难发现的失效。

所以这里只做一件事：凡是叫 `*_integration_test.go` 的文件，
必须带 `//go:build integration`。缺了就失败。

它不检查测试内容，也不保证测试会通过——那是 `make test-integration`
连上真库之后的事。这里只保证"它至少会被编译"。

用法：
    python3 scripts/check/integration_tag.py
"""
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
SUFFIX = "_integration_test.go"
TAG = "//go:build integration"


def main() -> int:
    missing: list[str] = []
    found = 0
    for path in sorted(ROOT.rglob(f"*{SUFFIX}")):
        if "node_modules" in path.parts or ".tmp" in path.parts:
            continue
        found += 1
        head = "".join(path.read_text().splitlines(keepends=True)[:5])
        if TAG not in head:
            missing.append(path.relative_to(ROOT).as_posix())

    if missing:
        print(f"以下集成测试缺少 `{TAG}`，默认构建不会编译它们：", file=sys.stderr)
        for m in missing:
            print(f"  {m}", file=sys.stderr)
        print(
            "\n要么补上 build tag，要么改用普通单测名——"
            "被隔离却没被 tag 的文件等于不存在。",
            file=sys.stderr,
        )
        return 1
    print(f"集成测试 build tag 检查通过（{found} 个文件）。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
