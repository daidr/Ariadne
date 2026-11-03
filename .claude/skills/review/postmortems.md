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
