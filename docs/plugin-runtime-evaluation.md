# 插件运行时比较

2026-11-18，Windows amd64，Go 1.27.4。这是选型阶段的独立实验。
Go 后端已采用 Lua 作为第三方插件接口，内置 provider 直接使用 Go。
正式接口见 [Lua 插件](lua-plugins.md)。

## 测量结果

每个样本使用新进程，加载前后均执行 GC 和 FreeOSMemory。内存为 Windows
GetProcessMemoryInfo 的 PrivateUsage 增量，即额外私有提交内存，不能当作物理内存。
独立进程模式加总所有子进程；WASM 的 distinct 模式使用不同模块内容，避免将同一个
编译模块的多实例共享误当成多个独立插件的成本。

| 10 个空载实例/模块                 | 额外私有提交内存 | 冷加载总耗时 |
| ---------------------------------- | ---------------: | -----------: |
| GopherLua 1.1.2                    |         1.78 MiB |      0.60 ms |
| 小型 WASM，10 个不同模块           |         2.43 MiB |      1.55 ms |
| 标准 Go 编译的 WASM，10 个不同模块 |       143.08 MiB |      3088 ms |
| 10 个独立原生 Go 进程              |       120.42 MiB |       312 ms |

同一标准 Go WASM 模块复用 10 个实例时为 46.45 MiB、330 ms，不能用这组结果代表
10 个不同插件。小型手写 WASM 文件 136 字节；同样导出函数的标准 Go WASM 文件约 1.80 MiB。
实验没有安装 TinyGo，未比较 TinyGo、Rust、AssemblyScript 等工具链。

Lua 禁用标准库并缩小栈和注册表；WASM 只实现算术、回调和死循环；进程只做 stdio JSON。
这些结果是合成空载样本，真实插件的网络连接、业务数据、依赖和并发请求会增加开销。
Windows 对极短时长的采样会有量化误差，不能据此承诺亚毫秒加载性能。

20 ms 超时的死循环实验中，GopherLua SetContext 和 wazero 的 context 取消均生效。
卸载后 GC 没有精确返回基线；运行时保留分配和采样误差需要与持续增长的泄漏区分，
本次单轮实验不能证明重复加载后的内存稳定性。没有测试进程模式的取消延迟。

原始结果和实验源码暂存在本机 `dist/qa/plugin-runtime-benchmark.json` 和
`dist/plugin-runtime-bench`，这两个位置不进入 Git。结果尚未在其他机器或平台复测。

## 选择依据

Lua 便于直接编辑脚本，且空载开销低。已用真实 Lua 脚本配合模拟 HTTP 服务验证流式输出、
取消和卸载。需要更严格的线性内存限制时，WASM 仍可作为后续选项。

- [GopherLua](https://github.com/yuin/gopher-lua) 的 LState 需要串行使用，SetContext 可中断执行。
  [SetMx 的实现](https://github.com/yuin/gopher-lua/blob/v1.1.2/state.go)读取整个进程的
  runtime.MemStats，超限会 os.Exit，不能用于单插件内存限额。
- [wazero RuntimeConfig](https://pkg.go.dev/github.com/tetratelabs/wazero#RuntimeConfig)
  提供 WithMemoryLimitPages 和 WithCloseOnContextDone。线性内存限额不包含编译器、
  宿主回调、HTTP 缓冲等所有分配，这些部分仍需单独限制。
- [Go 的 WASM reactor 支持](https://go.dev/blog/wasmexport)可以导出函数供宿主调用，
  但普通 Go 编译器会带入 Go 运行时；本次实测不支持“换成 Go WASM 就一定更省”的判断。

另外试验了 GoLua 0.4.1，其配额机制使用近似累计分配计数。
所用 count hook 没有在 20 ms 取消时中断空循环，最终约 2.3 秒触发 CPU 配额。
这不算取消测试通过；该试验未纳入推荐表，也未证明其内存配额能满足本项目需求。
参见 [GoLua quotas](https://github.com/arnodel/golua/blob/v0.4.1/quotas.md)。
