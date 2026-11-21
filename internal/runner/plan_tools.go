package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
		specification("update_plan", "Change the plan with one reason in the user's language. Use refs within this call. Changes apply immediately and are recorded in revision history.",
			args{"reason": field("string"), "ops": arrayOf(opSchema)}, []string{"reason", "ops"}, turn.updatePlan),
		specification("revert_plan_change", "Undo an earlier plan revision.",
			args{"revisionId": field("string"), "reason": field("string")}, []string{"revisionId", "reason"}, turn.revertPlan),
	}
}

func (turn *turn) readPlanTool() tool {
	return specification("read_plan", "Read the project's plan, dependencies, status, related threads and recent changes.", args{}, nil, turn.readPlan)
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
		Author: "coordinator", Reason: reason, Ops: ops, UniqueAdds: true,
	})
	if err != nil {
		return "", err
	}
	return reply(args{"status": "applied", "revisionId": change["revisionId"], "ids": refs}, nil)
}

func (turn *turn) revertPlan(ctx context.Context, input args) (string, error) {
	reason := text(input, "reason")
	change, err := turn.Repository.RevertPlanRevision(ctx, turn.conversation.ProjectID, text(input, "revisionId"),
		"coordinator", reason)
	if err != nil {
		return "", err
	}
	return reply(args{"status": "reverted", "revisionId": change["revisionId"]}, nil)
}
