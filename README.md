# Ariadne

一个自己使用的 AI 协作工具：你和协调器对话，协调器把需求拆成计划图并安排 threads 去调研、实现、修复，你随时能看到每件事在做什么、在等什么。

设计取舍见 [设计哲学](./docs/philosophy.md)，领域概念与分层见 [架构](./docs/architecture.md)。

> 早期开发中，目前只能在本机浏览器里使用。

## 开发

需要 [Bun](https://bun.sh) 1.4 及以上。

```sh
bun install
bun run dev      # 同时启动本地服务（127.0.0.1:7428）和前端开发服务器（localhost:5173）
bun run check    # 格式、lint、类型检查和测试
```

本地服务第一次启动时会在 `~/.ariadne/token` 生成访问令牌，并在终端打印带令牌的链接。开发时把链接里的 `#token=…` 接到 `http://localhost:5173/` 后面打开即可；`bun run build` 之后，本地服务会直接提供构建好的页面。

| 环境变量        | 作用                   | 默认值       |
| --------------- | ---------------------- | ------------ |
| `ARIADNE_HOME`  | 数据库与令牌所在的目录 | `~/.ariadne` |
| `ARIADNE_PORT`  | 本地服务的端口         | `7428`       |
| `ARIADNE_TOKEN` | 指定访问令牌，不读文件 | —            |

## 包

| 包                    | 内容                                         |
| --------------------- | -------------------------------------------- |
| `@ariadne/i18n`       | 文案与翻译，服务端和前端共用                 |
| `@ariadne/core`       | 领域模型、SQLite 事件存储与投影              |
| `@ariadne/plugin-sdk` | 插件、上下文 provider 与模型 provider 的接口 |
| `@ariadne/server`     | 本地 HTTP 服务、鉴权与事件流                 |
| `@ariadne/web`        | 浏览器界面                                   |
