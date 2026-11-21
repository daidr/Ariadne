package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/jsonutil"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/transcript"
	"github.com/daidr/Ariadne/sdk"
)

var mcpInvalidName = regexp.MustCompile("[^a-zA-Z0-9_-]")

func mcpName(server, tool string) string {
	clean := mcpInvalidName.ReplaceAllString(tool, "_")
	name := "mcp__" + server + "__" + clean
	if len(name) <= 64 && clean == tool {
		return name
	}
	sum := sha256.Sum256([]byte(tool))
	return fmt.Sprintf("%s_%x", name[:min(len(name), 57)], sum[:3])
}

func mcpMark(config mcp.ServerConfig, tool, role string) string {
	mark := config.Tools[tool]
	if mark == "" {
		mark = "ask"
	}
	if role == "coordinator" && mark != "read" {
		return "off"
	}
	if mark != "read" && mark != "ask" {
		return "off"
	}
	return mark
}

func selectedMCP(project core.ProjectView, id string) bool {
	for _, current := range items(project.MCP) {
		if current == id {
			return true
		}
	}
	return false
}

func (turn *turn) mcpTools(ctx context.Context, project core.ProjectView) []tool {
	ids := items(project.MCP)
	if len(ids) == 0 || turn.MCP == nil {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	type page struct {
		config mcp.ServerConfig
		tools  []mcp.ToolInfo
	}
	pages := make([]page, min(32, len(ids)))
	var group sync.WaitGroup
	running := make(chan struct{}, 8)
	for i, id := range ids[:len(pages)] {
		name, ok := id.(string)
		if !ok {
			continue
		}
		group.Go(func() {
			select {
			case <-listCtx.Done():
				return
			case running <- struct{}{}:
				defer func() { <-running }()
			}
			config, found, err := turn.MCP.Config(name)
			if err != nil || !found {
				return
			}
			specs, err := turn.MCP.Tools(listCtx, config)
			if err == nil {
				pages[i] = page{config, specs}
			}
		})
	}
	group.Wait()
	var tools []tool
	byName := map[string]tool{}
	var listing strings.Builder
	specBytes := 0
	for _, page := range pages {
		for _, spec := range page.tools {
			if len(tools) >= 256 {
				break
			}
			mark := mcpMark(page.config, spec.Name, turn.conversation.Role)
			if mark == "off" {
				continue
			}
			base := mcpName(page.config.ID, spec.Name)
			name := base
			for n := 2; byName[name].spec.Name != ""; n++ {
				name = fmt.Sprintf("%s_%d", base[:min(len(base), 60)], n)
			}
			description := ""
			if spec.Description != nil {
				description = textutil.HeadUnits(*spec.Description, 2000)
			}
			description = transcript.AsOutside(mcp.Redact(description, mcp.SecretsOf(page.config)))
			schema := spec.InputSchema
			if data, err := json.Marshal(schema); err != nil || len(data) > 20000 {
				schema = args{"type": "object", "properties": args{}}
			}
			entry := tool{
				spec: sdk.Tool{Name: name, Description: "External tool " + name + ". " + description, Parameters: schema},
				call: func(ctx context.Context, input args) (string, []any, error) {
					return turn.callMCP(ctx, page.config, spec.Name, mark, input)
				},
			}
			data, _ := json.Marshal(entry.spec)
			specBytes += len(data)
			tools = append(tools, entry)
			byName[name] = entry
			line, _, _ := strings.Cut(description, "\n")
			fmt.Fprintf(&listing, "- %s: %s\n", name, textutil.HeadUnits(line, 160))
		}
	}
	if specBytes <= 20000 {
		return tools
	}
	describe := specification("mcp_describe", "Read the parameters of external tools listed by mcp_call.",
		args{"names": arrayOf(field("string"))}, []string{"names"}, func(_ context.Context, input args) (string, error) {
			names, err := stringsOf(input["names"])
			if err != nil || len(names) == 0 {
				return "", errors.New("provide the external tool names to look up")
			}
			specs := []sdk.Tool{}
			for _, name := range names {
				if entry, ok := byName[name]; ok {
					specs = append(specs, entry.spec)
				}
			}
			return reply(specs, nil)
		})
	call := specification("mcp_call", "Call an external tool after reading its parameters with mcp_describe.\n"+listing.String(),
		args{"name": field("string"), "arguments": field("object")}, []string{"name"}, nil)
	call.call = func(ctx context.Context, input args) (string, []any, error) {
		entry, ok := byName[text(input, "name")]
		if !ok {
			return "", nil, errors.New("unknown external tool")
		}
		passed, _ := input["arguments"].(args)
		if passed == nil {
			passed = args{}
		}
		return entry.call(ctx, passed)
	}
	return []tool{describe, call}
}

func (turn *turn) callMCP(ctx context.Context, config mcp.ServerConfig, name, mark string, input args) (string, []any, error) {
	conversation := turn.conversation
	project, err := turn.Repository.GetProject(ctx, conversation.ProjectID)
	if err != nil {
		return "", nil, err
	}
	current, found, err := turn.MCP.Config(config.ID)
	if err != nil || !found || !selectedMCP(project, config.ID) || !reflect.DeepEqual(current, config) {
		return "", nil, errors.New("external tool configuration changed")
	}
	secrets := mcp.SecretsOf(config)
	if mark == "ask" {
		shown, err := json.MarshalIndent(input, "", "  ")
		if err != nil || len(shown) > 10000 {
			return "", nil, errors.New("tool arguments are too long for approval")
		}
		key, err := jsonutil.Encode(input)
		if err != nil {
			return "", nil, err
		}
		what := "mcp:" + config.ID + "/" + name
		purpose := fmt.Sprintf("%s:%x", what, sha256.Sum256(key))
		approval, err := turn.Repository.CallApproval(ctx, project.ID, conversation.ID, purpose, func() map[string]any {
			title := "Allow this external tool once?"
			yes, no := "Run", "Don't run"
			if turn.Settings.Locale() != "en" {
				title, yes, no = "允许调用这个外部工具一次？", "运行", "不要"
			}
			return args{"type": "confirm", "title": title, "description": textutil.HeadUnits(config.Name+" / "+name, 300),
				"detail": mcp.Redact(string(shown), secrets), "approveText": yes, "denyText": no, "tone": "neutral"}
		}, core.CallApprovalOptions{})
		if err != nil {
			return "", nil, err
		}
		if approval.State == "declined" {
			return "", nil, errors.New("the user declined this external tool call")
		}
		if approval.State != "approved" {
			return "Waiting for the user's approval. End your turn until their answer arrives.", nil, nil
		}
		if err := turn.Repository.UseApproval(ctx, project.ID, conversation.ID, approval.AttentionID, what); err != nil {
			return "", nil, err
		}
	}
	result, err := turn.MCP.CallTool(ctx, config, name, input, 2*time.Minute)
	if err != nil {
		return "", nil, errors.New(mcp.Redact(err.Error(), secrets))
	}
	var output strings.Builder
	var attachments []any
	store := files.Store{Events: turn.Repository.Store}
	for _, raw := range result.Content {
		if output.Len() >= 50000 {
			break
		}
		var part struct {
			Type, Text, MIMEType, Data, Name, URI string
			Resource                              struct{ URI, Text string }
		}
		if json.Unmarshal(raw, &part) != nil {
			continue
		}
		switch part.Type {
		case "text":
			output.WriteString(textutil.HeadUnits(mcp.Redact(part.Text, secrets), 50000))
		case "image":
			if len(attachments) < 2 && len(part.Data) <= base64.StdEncoding.EncodedLen(files.MaxImageBytes) && files.IsImage(part.MIMEType) {
				if decoded, err := base64.StdEncoding.DecodeString(part.Data); err == nil {
					if stored, err := store.Save(ctx, project.ID, part.MIMEType, decoded, false); err == nil {
						attachments = append(attachments, args{"fileId": stored.ID, "name": "image", "mime": stored.MIME, "size": stored.Size})
						output.WriteString("[Image attached]")
					}
				}
			}
		case "resource":
			output.WriteString(textutil.HeadUnits(mcp.Redact(part.Resource.URI+"\n"+part.Resource.Text, secrets), 50000))
		case "resource_link":
			output.WriteString(textutil.HeadUnits(mcp.Redact(part.Name+" "+part.URI, secrets), 50000))
		case "audio":
			output.WriteString("[Audio returned]")
		}
		output.WriteString("\n")
	}
	content := strings.TrimSpace(output.String())
	if content == "" {
		content = textutil.HeadUnits(mcp.Redact(string(result.StructuredContent), secrets), 50000)
	}
	block := transcript.Fenced("tool output", content, "")
	if result.IsError {
		return "The external tool reported an error.\n" + block.Text, attachments, errors.New("the external tool reported an error")
	}
	return block.Text, attachments, nil
}
