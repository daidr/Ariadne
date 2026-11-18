package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/google/uuid"
)

func (turn *turn) planTools() []tool {
	opSchema := args{"type": "object", "required": []string{"op"}, "properties": args{
		"op":  args{"type": "string", "enum": []string{"add", "update", "remove", "link", "unlink"}},
		"ref": args{"type": "string", "description": "add: temporary name to reference a new node within this call."},
		"id":  field("string"), "kind": args{"type": "string", "enum": []string{"task", "milestone", "decision"}},
		"title": field("string"), "description": field("string"), "acceptance": field("string"),
		"dependsOn": arrayOf(field("string")), "from": field("string"), "to": field("string"),
	}}
	return []tool{
		turn.readPlanTool(),
		specification("update_plan", "Change the plan with one reason in the user's language. Use refs within this call. Removing nodes, changing acceptance or modifying milestones is sent to the user for approval.",
			args{"reason": field("string"), "ops": arrayOf(opSchema)}, []string{"reason", "ops"}, turn.updatePlan),
		specification("revert_plan_change", "Undo an earlier plan revision. Changes that require approval follow the same rules as update_plan.",
			args{"revisionId": field("string"), "reason": field("string")}, []string{"revisionId", "reason"}, turn.revertPlan),
	}
}

func (turn *turn) readPlanTool() tool {
	return specification("read_plan", "Read the project's plan, dependencies, status, related threads and proposals.", args{}, nil, turn.readPlan)
}

func (turn *turn) readPlan(ctx context.Context, _ args) (string, error) {
	return reply(turn.Repository.ModelPlan(ctx, turn.conversation.ProjectID))
}

func resolvePlanOps(raw any) ([]map[string]any, map[string]string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 256 {
		return nil, nil, errors.New("ops must contain between 1 and 256 changes")
	}
	refs := map[string]string{}
	assigned := make([]string, len(items))
	for index, item := range items {
		op, ok := item.(args)
		if !ok {
			return nil, nil, fmt.Errorf("ops[%d] must be an object", index)
		}
		kind, ok := op["op"].(string)
		if !ok {
			return nil, nil, fmt.Errorf("ops[%d].op must name a plan operation", index)
		}
		if kind != "add" {
			continue
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, nil, err
		}
		assigned[index] = "nod_" + id.String()
		if ref := text(op, "ref"); ref != "" {
			if refs[ref] != "" {
				return nil, nil, fmt.Errorf("ref %q is used twice", ref)
			}
			refs[ref] = assigned[index]
		}
	}
	resolve := func(raw any) any {
		key, ok := raw.(string)
		if id := refs[strings.TrimSpace(key)]; ok && id != "" {
			return id
		}
		return raw
	}
	result := make([]any, 0, len(items))
	links := []any{}
	for index, item := range items {
		op := item.(args)
		clean := args{"op": op["op"]}
		switch op["op"] {
		case "add":
			clean["id"] = assigned[index]
			clean["kind"] = op["kind"]
			if _, ok := op["title"].(string); !ok || text(op, "title") == "" {
				return nil, nil, fmt.Errorf("ops[%d].title is required", index)
			}
			if op["dependsOn"] != nil {
				dependencies, ok := op["dependsOn"].([]any)
				if !ok || len(links)+len(dependencies) > 512 {
					return nil, nil, fmt.Errorf("ops[%d].dependsOn must be an array within the dependency limit", index)
				}
				for _, parent := range dependencies {
					links = append(links, args{"op": "link", "from": resolve(parent), "to": assigned[index]})
				}
			}
		case "update", "remove":
			clean["id"] = resolve(op["id"])
		case "link", "unlink":
			clean["from"], clean["to"] = resolve(op["from"]), resolve(op["to"])
		}
		for _, key := range []string{"title", "description", "acceptance"} {
			if value, present := op[key]; present {
				clean[key] = value
			}
		}
		result = append(result, clean)
	}
	ops, err := core.ParsePlanOps(append(result, links...))
	return ops, refs, err
}

func (turn *turn) updatePlan(ctx context.Context, input args) (string, error) {
	ops, refs, err := resolvePlanOps(input["ops"])
	if err != nil {
		return "", err
	}
	reason := text(input, "reason")
	change, err := turn.Repository.RevisePlan(ctx, turn.conversation.ProjectID, core.PlanRevision{
		Author: "coordinator", Reason: reason, Ops: ops, UniqueAdds: true, Confirmation: planConfirmation(turn.Settings.Locale(), reason),
	})
	if err != nil {
		return "", err
	}
	if change["status"] == "applied" {
		return reply(args{"status": "applied", "revisionId": change["revisionId"], "ids": refs}, nil)
	}
	return reply(args{"status": "waiting for the user to agree", "proposalId": change["proposalId"], "ids": refs,
		"note": "Nodes added by this change do not exist until the user agrees."}, nil)
}

func (turn *turn) revertPlan(ctx context.Context, input args) (string, error) {
	reason := text(input, "reason")
	change, err := turn.Repository.RevertPlanRevision(ctx, turn.conversation.ProjectID, text(input, "revisionId"),
		"coordinator", reason, planConfirmation(turn.Settings.Locale(), reason))
	if err != nil {
		return "", err
	}
	if change["status"] == "applied" {
		return reply(args{"status": "reverted", "revisionId": change["revisionId"]}, nil)
	}
	return reply(args{"status": "waiting for the user to agree", "proposalId": change["proposalId"]}, nil)
}

var planTranslations = map[string][2]string{
	"plan.op.add":               {"新增「{title}」", "Add \"{title}\""},
	"plan.op.addMilestone":      {"新增里程碑「{title}」", "Add milestone \"{title}\""},
	"plan.op.remove":            {"删除「{title}」", "Remove \"{title}\""},
	"plan.op.removeWithThreads": {"删除「{title}」，上面的 {count} 个讨论串会暂停", "Remove \"{title}\"; its {count} threads will pause"},
	"plan.op.acceptance":        {"把「{title}」的验收标准改为：{value}", "Change the acceptance of \"{title}\" to: {value}"},
	"plan.op.rename":            {"把「{title}」改名为「{value}」", "Rename \"{title}\" to \"{value}\""},
	"plan.op.update":            {"修改「{title}」的说明", "Edit the description of \"{title}\""},
	"plan.op.link":              {"「{to}」依赖「{from}」", "\"{to}\" depends on \"{from}\""},
	"plan.op.unlink":            {"「{to}」不再依赖「{from}」", "\"{to}\" no longer depends on \"{from}\""},
}

func planConfirmation(locale, reason string) core.PlanConfirmation {
	return func(changes []map[string]any) (map[string]any, error) {
		lang := 0
		title, apply, keep, more := "协调器想修改计划："+reason, "同意修改", "保持原样", "…还有 %d 项，在计划图上预览可以看到全部"
		if locale == "en" {
			lang = 1
			title, apply, keep, more = "The coordinator wants to change the plan: "+reason, "Apply the change", "Keep as is", "…and %d more; preview on the plan to see them all"
		}
		lines := make([]string, 0, len(changes))
		units := 0
		for _, change := range changes {
			key, _ := change["key"].(string)
			params, _ := change["params"].(map[string]any)
			pairs := make([]string, 0, len(params)*2)
			for name, value := range params {
				pairs = append(pairs, "{"+name+"}", fmt.Sprint(value))
			}
			line := "• " + clipPlanText(strings.NewReplacer(pairs...).Replace(planTranslations[key][lang]), 2000)
			units += len(utf16.Encode([]rune(line))) + 1
			if units > 9900 {
				lines = append(lines, fmt.Sprintf(more, len(changes)-len(lines)))
				break
			}
			lines = append(lines, line)
		}
		return core.ConfirmBlock(args{"title": clipPlanText(title, 2000), "detail": strings.Join(lines, "\n"), "approveText": apply, "denyText": keep}), nil
	}
}

func clipPlanText(text string, maxUnits int) string {
	units := 0
	for at, r := range text {
		units += utf16.RuneLen(r)
		if units >= maxUnits {
			return text[:at] + "…"
		}
	}
	return text
}
