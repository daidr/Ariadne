# Lua 插件

Go 后端加载绝对路径指向的 `.lua` 文件，也可指定包含 `plugin.lua` 的目录。文件返回一个表，
包含 `version = 1`、唯一的 `id`、本地化 `name` 以及一个或多个 provider。标识可包含字母、
数字、点、下划线和连字符，并以字母或数字开头。名称和标题需要 `zh-CN`，可同时提供 `en`。

```lua
local title = { ["zh-CN"] = "文档搜索", en = "Docs search" }
local origin = "https://search.example.com"

return {
  version = 1,
  id = "docs-search",
  name = title,
  network = { origin },
  searches = {{
    kind = "docs-search",
    title = title,
    search = function(query, limit)
      local response = ariadne.http({
        url = origin .. "/search",
        method = "POST",
        headers = { ["Content-Type"] = "application/json" },
        body = ariadne.json_encode({ query = query, limit = limit }),
      })
      assert(response.status == 200, "搜索服务返回 " .. response.status)
      return ariadne.json_decode(response.body).results
    end,
  }},
}
```

各组都是数组。每个 provider 需要 `kind` 和 `title`。模型、搜索、来源和仪表盘可以声明
`settings = { type = "object", properties = { ... }, required = { ... } }`，字段可用
`secret = true` 标记密钥，密钥字段不能提供默认值。

插件也可以在返回表的顶层声明同样的 `settings` schema。这组字段会出现在「设置 → 插件」
的独立设置窗口里。回调使用 `ariadne.settings()` 读取该插件当前的设置。来源若依赖必填的插件
配置，可以声明 `requiresPluginSettings = true`，设置齐全后即可在来源选择器里选用。

| 组           | 回调                                                                                                                                                                                                                     |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `searches`   | `search(query, limit, settings)` 返回 `{title, url, snippet}` 组成的数组或 `{answer, results}` 对象；可选 `check(settings)` 用于连接测试（返回布尔值或 `{ok, status, message}`）。支持声明 `settings` 与 `description`。 |
| `models`     | `stream(settings, request, emit)` 调用 `emit(part)`，结束时发送 `{type="finish", reason="stop"}`。可选 `models(settings, headers)`、`check(settings)`、`describe(settings, request)` 和 `search(settings, request)`。    |
| `dashboards` | `fetch(settings, projectId)` 返回键名对应各组数据的表。                                                                                                                                                                  |
| `links`      | `match(url)` 返回标签或 `nil`，`preview(url)` 返回预览对象或 `nil`。                                                                                                                                                     |
| `contexts`   | 提供只读引用来源。`list(settings, path)` 返回 `{name, kind}` 数组，`kind` 为 `file` 或 `directory`。`read(settings, path, maxBytes)` 返回 Lua 字节串。可选 `check(settings)`、`browse(settings, folder)`。               |

模型请求、流式事件和返回对象字段沿用 [Go SDK 的 JSON 字段](../sdk/model.go)。
`describe` 的结果会进入请求追踪，应省略密钥。使用 `ariadne.array()` 表示空数组，
使用 `ariadne.null` 在表里保存 JSON 的 `null`。普通空表会编码为 JSON 对象。

`ariadne.http({url, method, headers, body})` 返回 `{status, headers, body}`。
传入第二个参数 `function(chunk, status)` 可逐块读取响应，此时返回结果省略 `body`。
回调收到的是原始字节片段，SSE 和其他流协议需要在脚本里按各自的分隔符组帧。
请求只访问 `network` 中列出的协议、主机和端口，返回重定向响应时由脚本处理。
插件声明阶段、链接匹配和模型请求描述阶段不执行 HTTP。

插件脚本加载时编译，每次调用在独立 Lua 状态里运行，顶层声明会重新执行。
全局变量和闭包状态的生命周期是单次调用。脚本可使用基础函数和 `table`、`string`、
`math`，没有文件、进程或环境变量接口。宿主对调用时长、并发、HTTP 和交换数据设有上限，
取消和卸载会中断正在执行的 Lua 或 HTTP 请求。Lua 运行时使用 Go 堆，插件应来自信任的来源。

自定义可写工作区、Git 托管和命令执行目前使用原生 Go provider 接口。
Go 默认启动入口仍在迁移中，当前 Lua 接口通过原生服务和 API 测试验证。
