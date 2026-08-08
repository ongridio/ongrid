// dummy_main.go 是 issue #21 Step 0 spike 的测试辅助程序。
//
// 与 sleeper_main.go 的区别：dummy 支持 --version（验证 W5 smokeTestVersion
// 前置）和 --self-rename（验证运行中进程能否 rename 自己的 .exe）。
//
// 三种模式：
//   - 默认：sleep 60s 然后退出 0（模拟运行中的 supervisor.exe 被外部 rename）
//   - --version：输出 "ongrid-dummy v0.0.1" 并退出 0（模拟 supervisor --version）
//   - --self-rename <path>：尝试 os.Rename(path, path+".old")，
//     通过 stdout JSON 报告结果后立即退出（验证进程内自 rename 语义）
//
// 与 sleeper_main.go 一样，通过 `go build -o dummy.exe testdata/dummy_main.go`
// 显式编译（testdata 不参与包编译）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	version := flag.Bool("version", false, "print version and exit")
	selfRename := flag.String("self-rename", "", "rename this binary to <path>.old then report result and exit")
	flag.Parse()

	if *version {
		fmt.Println("ongrid-dummy v0.0.1")
		return
	}

	if *selfRename != "" {
		oldPath := *selfRename + ".old"
		err := os.Rename(*selfRename, oldPath)
		result := map[string]any{
			"op":  "self_rename",
			"src": *selfRename,
			"dst": oldPath,
		}
		if err != nil {
			result["ok"] = false
			result["err"] = err.Error()
		} else {
			result["ok"] = true
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	// 默认：持续运行 60s 模拟 supervisor 进程持有自身 .exe 文件锁
	time.Sleep(60 * time.Second)
}
