#!/usr/bin/env python3
"""校验 OpenAPI 文档与代码注册的端点是否一致。

只校验一个方向：文档里声明的端点必须真的注册了。
文档多写比文档少写更危险——调用方（人和程序 alike）会照着不存在的接口排查。
反方向"已注册但文档没写"属于补全工作，单独跟踪，不在此处阻断。

用法：openapi_check.py <文档路径> <路由前缀>
"""
import json
import re
import subprocess
import sys

doc_path, prefix = sys.argv[1], sys.argv[2]
doc = json.load(open(doc_path))
out = subprocess.run(
    ["grep", "-rhoE", '"/api/v1/' + prefix + '[^"]*"',
     "--include=*.go", "--exclude=*_test.go", "internal/", "cmd/"],
    capture_output=True, text=True).stdout
registered = {re.sub(r":[A-Za-z_]+", "{id}", m)
              for m in re.findall(r'"(/api/v1/' + prefix + r'[^"]*)"', out)}
registered.discard("/api/v1/" + prefix + "/")

phantom = [path for path in sorted(doc.get("paths", {}))
           if re.sub(r"\{[^}]+\}", "{id}", path) not in registered]
for path in phantom:
    print(path)

# 悬空引用：文档里 $ref 指向一个不存在的 schema，展开就会断。
schemas = set(doc.get("components", {}).get("schemas", {}))
refs = set(re.findall(r"#/components/schemas/([A-Za-z0-9_]+)", json.dumps(doc)))
for name in sorted(refs - schemas):
    print("schema:" + name)

sys.exit(1 if (phantom or refs - schemas) else 0)
