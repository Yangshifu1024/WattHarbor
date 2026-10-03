#!/usr/bin/env python3
"""把某个 use case 包里对 database 包的引用补齐，并把已迁走的符号改指向新包。

用法：python3 scripts/check/relink.py <新包目录> [更多包目录...]

背景：use case 迁移时，文件从 package database 搬到独立包，原本同包可见的
符号现在需要显式限定。这个脚本只做"补限定符"这一件事，不移动文件、不改逻辑。

关键约束（都是踩过的坑）：
  - 只接受 database 包的**顶层**符号（type/func/var/无接收者的方法），
    误把方法当符号会加上不存在的前缀。
  - 结构体字面量里的字段名不能加前缀，`{Page: 1}` 不是 `database.Page: 1`。
  - 包内测试不 import 自身。
  - 每轮从编译器取实际报错，不靠预先猜测。

判据：脚本跑到 `go build ./...` 通过即退出 0；超过指定轮数仍未通过则退出 1。
"""
import pathlib
import re
import subprocess
import sys

DB = "internal/infrastructure/database"
DBQ = '"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"'


def top_level_symbols(directory: str) -> set:
    """包目录下的顶层符号。方法（带接收者）不收——它们不是包级符号。"""
    out = set()
    for p in pathlib.Path(directory).glob("*.go"):
        if p.name.endswith("_test.go"):
            continue
        t = p.read_text()
        out |= set(re.findall(r"^type\s+(\w+)", t, re.M))
        out |= set(re.findall(r"^func\s+(\w+)\s*[\(\[]", t, re.M))  # 无接收者
        out |= set(re.findall(r"^var\s+(\w+)", t, re.M))
        out |= set(re.findall(r"^\s*(\w+)\s*=\s*errors\.New", t, re.M))
        # 分组声明里的类型：type ( X struct{ ... } )
        out |= set(re.findall(r"^\t(\w+)\s+(?:struct|interface|\[\])", t, re.M))
    return out


STRUCT_FIELD_DECL = re.compile(r"^\s*(\w+)\s+[\w.*\[\]{}]+\s*(//.*)?$")
STRUCT_FIELD_KEY = re.compile(r"(\w+)\s*:\s*")


def add_prefix(text: str, syms) -> str:
    """给 syms 加 database. 前缀，跳过结构体字段名。

    按行判断而不是靠正则猜测：Go 的结构体字段声明是"名字 类型"独占一行，
    字面量字段名后面跟冒号。早先的正则只防了字面量、漏了声明，把
    gateway/store 里的本地字段 `Page int` 改成了 `database.Page int`——
    那个包里根本没有 database 引用。
    """
    out = []
    for line in text.splitlines(keepends=True):
        stripped = line.lstrip()
        if STRUCT_FIELD_DECL.match(stripped) and any(
            re.match(rf"^{s}\s", stripped) for s in syms
        ):
            out.append(line)  # 结构体字段声明，原样保留
            continue
        for s in sorted(syms):
            # 排除行首（字段名）与 `.` 之后（选择器/方法）
            line = re.sub(rf"(?<![\w.]){s}\b(?![\w]*\s*:)", "database." + s, line)
            line = re.sub(rf"(?<![\w.])(?<!^)\b{s}\b(?=\s*:)", s, line)
        out.append(line)
    text = "".join(out)
    # 字面量字段键：Foo{..., database.Page: 1}
    text = re.sub(r"\bdatabase\.(\w+):", r"\1:", text)
    return text


def relink(target_pkg: str, max_rounds: int = 30) -> bool:
    """把 database.<sym> 改成 <target_pkg>.<sym>。

    判据：sym 必须是**目标包的顶层符号**。文件刚从 database 搬到独立包时，
    调用方还写着 database.Sym——而 Sym 已经不在 database 里了，这正是要
    改写的情况。所以 todo 取目标包自己的符号表，不是 database 的差集。

    反过来，目标包内部的同名符号（同包可见）不能加前缀，否则会写出
    `database.X` 这种悬空引用。
    """
    imp = f'"github.com/ChargePilot2026/charge-pilot/internal/{target_pkg}"'
    own = top_level_symbols(f"internal/{target_pkg}")
    for rnd in range(1, max_rounds + 1):
        out = subprocess.run(
            ["go", "build", "./..."], capture_output=True, text=True
        ).stderr
        undef = {
            m.split(".")[-1] for m in re.findall(r"undefined: ([\w.]+)", out)
        }
        if not undef:
            print(f"  符号收敛（第 {rnd} 轮）")
            break
        todo = undef & own
        if not todo:
            print(f"  剩余未定义且不属于 {target_pkg}: {sorted(undef)}")
            return False
        for p in pathlib.Path(".").rglob("*.go"):
            s = str(p)
            if s.startswith(".tmp") or "/vendor/" in s:
                continue
            # domain 是零框架依赖层，永不引 database / persist / 任何实现包。
            # 脚本按名字匹配符号，会把 domain 自己的 Settlement 误认成
            # wallet.Settlement 而加前缀——那会直接破坏分层。
            if s.startswith("internal/domain/"):
                continue
            t = p.read_text()
            new = add_prefix(t, todo)
            if new == t:
                continue
            if imp not in new and f"{target_pkg}." in new and DBQ in new:
                new = new.replace(DBQ, DBQ + "\n\t" + imp)
            p.write_text(new)
        subprocess.run(["gofmt", "-w", "internal", "cmd"], capture_output=True)
        print(f"  第 {rnd} 轮：{target_pkg} <- {sorted(todo)}")
    else:
        return False
    return True


def drop_unused_imports() -> None:
    for _ in range(12):
        out = subprocess.run(
            ["go", "build", "./..."], capture_output=True, text=True
        ).stderr
        items = re.findall(
            r'([\w/.\-]+\.go):\d+:\d+: ("[^"]+") imported and not used', out
        )
        if not items:
            print("  未使用 import 已清空")
            return
        for f, im in items:
            p = pathlib.Path(f)
            if not p.exists():
                continue
            t = p.read_text()
            body = t.split(")", 1)[1] if ")" in t else t
            alias = re.search(r"(\w+)\s+" + re.escape(im), t)
            name = alias.group(1) if alias else im.strip('"').split("/")[-1]
            if re.search(rf"(?<![\w.]){re.escape(name)}\.", body):
                continue  # 仍在使用，编译器误报或同名前缀
            p.write_text(re.sub(rf"\n\t(?:\w+ )?{re.escape(im)}", "", t, count=1))
        subprocess.run(["gofmt", "-w", "internal", "cmd"], capture_output=True)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    for pkg in sys.argv[1:]:
        print(f"=== {pkg} ===")
        ok = relink(pkg)
        drop_unused_imports()
    sys.exit(0 if subprocess.run(["go", "build", "./..."],
                                 capture_output=True).returncode == 0 else 1)
