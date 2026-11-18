package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sandboxseed HOME EXAMPLE_REPO")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := seed(ctx, os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func seed(ctx context.Context, home, example string) error {
	database := filepath.Join(home, "ariadne.sqlite")
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		return errors.New("sandbox seed requires a fresh database")
	}
	repo, err := core.Open(ctx, core.Options{Path: database})
	if err != nil {
		return err
	}
	defer repo.Close()
	registry := sources.New()
	if _, err := registry.Register(local.DirectoryProvider(filepath.Join(home, "plugins", "local"), nil, false)); err != nil {
		return err
	}
	workspaceManager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = workspaceManager.Close(cleanup)
	}()
	project, err := repo.CreateProject(ctx, new("开发沙箱"), core.ProjectStart{
		Source: core.Source{Kind: "local-directory", Settings: map[string]any{"path": example}},
	})
	if err != nil {
		return err
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "修复讨论串卡片标题被变更统计、合并和还要改按钮挤压的问题"})
	if err != nil {
		return err
	}
	workspace, err := workspaceManager.ForThread(ctx, project.ID, thread.ID)
	if err != nil {
		return err
	}
	if err := workspace.WriteFile(ctx, "layout.txt", "标题独立显示\n变更和审核操作另起一行\n窄屏允许换行\n"); err != nil {
		return err
	}
	git := func(args ...string) (string, error) {
		argv := append([]string{"git", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=", "-c", "user.name=Ariadne Dev", "-c", "user.email=dev@example.invalid"}, args...)
		result, err := workspace.ExecFile(ctx, argv, sdk.ExecOptions{Timeout: 10 * time.Second})
		if err != nil {
			return "", err
		}
		if result.ExitCode != 0 {
			return "", errors.New(result.Stderr)
		}
		return strings.TrimSpace(result.Stdout), nil
	}
	from, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := git("add", "--", "layout.txt"); err != nil {
		return err
	}
	if _, err := git("commit", "-qm", "Improve thread layout"); err != nil {
		return err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	turn, err := repo.StartTurn(ctx, thread.ID, "mock")
	if err != nil {
		return err
	}
	file := core.TurnFile{Path: "layout.txt", Status: "added", Added: new(int64(3)), Removed: new(int64(0))}
	if err := repo.RecordTurnChanges(ctx, thread.ID, turn, from, head, []core.TurnFile{file}, &core.ChangeTotals{Files: 1, Added: 3}); err != nil {
		return err
	}
	if _, err := repo.ReportThread(ctx, thread.ID, core.ThreadReport{Kind: "ready", Headline: "布局调整已完成，等待查看并合并", Summary: "标题和操作分行，窄屏下自动换行。"}, &head, core.ReviewOptions{}); err != nil {
		return err
	}
	if err := repo.FinishTurn(ctx, thread.ID, turn, "completed", nil); err != nil {
		return err
	}
	report := func(title, kind, headline, summary string) (string, error) {
		item, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: title})
		if err != nil {
			return "", err
		}
		_, err = repo.ReportThread(ctx, item.ID, core.ThreadReport{Kind: kind, Headline: headline, Summary: summary}, nil, core.ReviewOptions{})
		return item.ID, err
	}
	research, err := report("检查长标题在侧栏、聊天内容区和窄屏中的可读性", "ready", "浏览器检查结果待确认", "检查标题完整显示和按钮可操作性。")
	if err != nil {
		return err
	}
	done, err := report("配置独立临时 settings 和本地模拟 AI 接口", "done", "开发环境已就绪", "可以发送消息验证真实接口链路。")
	if err != nil {
		return err
	}
	working, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "核查度量前端灰度残留"})
	if err != nil {
		return err
	}
	if _, err := repo.StartThread(ctx, working.ID); err != nil {
		return err
	}
	if _, err := repo.UpdateTodo(ctx, project.ID, working.ID, []core.TodoItem{
		{Label: "读取度量契约与仓库说明", Status: "doing"},
		{Label: "核查前端灰度逻辑", Status: "next"},
		{Label: "整理核查结果", Status: "next"},
	}); err != nil {
		return err
	}
	blocks := []any{}
	for _, id := range []string{working.ID, thread.ID, research, done} {
		blocks = append(blocks, map[string]any{"type": "thread", "threadId": id})
	}
	if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{ConversationID: project.CoordinatorID, Author: "coordinator",
		Content: "这里是开发沙箱。下面的讨论串可打开、查看变更和合并。发送消息可测试模拟 AI 的流式回复。", Blocks: blocks}); err != nil {
		return err
	}
	coordinator, err := repo.GetConversation(ctx, project.CoordinatorID)
	if err != nil {
		return err
	}
	turn, err = repo.StartTurn(ctx, coordinator.ID, "mock")
	if err != nil {
		return err
	}
	if err := repo.RecordStep(ctx, coordinator.ID, turn, core.Step{
		SawSeq: coordinator.PendingSeq, Input: core.StepInput{Tools: "[]", Call: "{}"}, Timing: core.CallTiming{StartedAt: time.Now().UnixMilli()},
	}); err != nil {
		return err
	}
	return repo.FinishTurn(ctx, coordinator.ID, turn, "completed", nil)
}
