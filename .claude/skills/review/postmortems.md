# 评审复盘记录

bug 在评审通过、合入之后才被发现时，在这里追加一条记录，并按 [SKILL.md](./SKILL.md) 的“复盘”一节更新检查清单。最新的记录放在最后。

格式：

```markdown
## YYYY-MM-DD：一句话描述 bug

- 引入的 PR：#
- 表现：
- 根因：
- 为什么评审没发现：
- 对 skill 的修改：
```

## 2026-11-03：整页主题手写在 body 上

- 引入的 PR：#2（合入前在实现中发现并修正）
- 表现：手写在 `body` 上的背景、文字色和字体 token 与 dou-ui 的主题切换不同步。
- 根因：dou-ui 的设计是不给宿主页面加样式，主题只作用于 `data-dou-theme` 之下；整页使用时应把 `data-dou-theme` 放在 `<html>` 上，或调用 `useColorMode()`。
- 为什么评审没发现：当时还没有评审 skill。
- 对 skill 的修改：在“UI 只用 dou-ui”里加入主题检查项。

## 2026-11-04：删掉 `error.stepLimit` 后，打开旧的失败 thread 页面崩溃

- 引入的 PR：#17（去掉步数上限时删了这条文案）；#18 的右侧 thread 面板把它显示了出来
- 表现：之前因超出步数而失败的 thread，升级后在右侧打开时，失败提示渲染抛出 `Cannot read properties of undefined (reading 'replace')`，接着 Vue 报 `resolveAsyncComponentPlaceholder` 读 null，整个面板停止更新。
- 根因：回合失败原因以 `LocalizedMessage` 存在事件日志和投影里，key 删掉后翻译函数拿到 `undefined` 再去 `format`。
- 为什么评审没发现：清单只要求新增文案两种语言齐全，没有要求删 key 前查它是否已存进事件；测试数据里也没有旧版本留下的失败回合。
- 对 skill 的修改：“i18n 中文优先”加一条，删除或改名的 key 若写进过事件就要保留。翻译函数遇到不认识的 key 时改为原样返回 key，不再抛错。
