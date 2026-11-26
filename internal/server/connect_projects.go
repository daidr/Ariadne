package server

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

var projectToolSwitches = []string{"fetch", "search", "commands", "research", "issueWrites"}

type projectService struct {
	api *API
}

func (api *API) registerProjectRPC() {
	svc := &projectService{api: api}
	api.mountConnect(
		ariadnev1.File_ariadne_v1_projects_proto.Services().ByName("ProjectService"),
		func(opts ...connect.HandlerOption) (string, http.Handler) {
			return ariadnev1connect.NewProjectServiceHandler(svc, opts...)
		},
	)
}

func (s *projectService) ListProjects(
	ctx context.Context,
	req *connect.Request[ariadnev1.ListProjectsRequest],
) (*connect.Response[ariadnev1.ListProjectsResponse], error) {
	ids := req.Msg.Ids
	if len(ids) > 128 {
		return nil, badInput("id")
	}
	repo := s.api.options.Repository
	seq, err := repo.Store.LastSeq(ctx)
	if err != nil {
		return nil, err
	}
	var projects []core.ProjectView
	if len(ids) > 0 {
		projects, err = repo.ListProjectsByID(ctx, ids)
	} else {
		projects, err = repo.ListProjects(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*ariadnev1.ProjectView, 0, len(projects))
	for _, project := range projects {
		out = append(out, toProtoProjectView(project))
	}
	return connect.NewResponse(&ariadnev1.ListProjectsResponse{
		Projects: out,
		LastSeq:  seq,
	}), nil
}

func (s *projectService) ListAllNeedsMe(
	ctx context.Context,
	_ *connect.Request[ariadnev1.ListAllNeedsMeRequest],
) (*connect.Response[ariadnev1.ListAllNeedsMeResponse], error) {
	all, err := s.api.options.Repository.ListAllNeedsMe(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*ariadnev1.AllNeedsMeItem, 0, len(all.Items))
	for _, entry := range all.Items {
		converted := toProtoNeedsMeItem(entry.Item)
		if converted == nil {
			continue
		}
		items = append(items, &ariadnev1.AllNeedsMeItem{
			ProjectId:   entry.ProjectID,
			ProjectName: entry.ProjectName,
			Item:        converted,
			At:          entry.At,
		})
	}
	threads := make([]*ariadnev1.NeedsMeThreadSummary, 0, len(all.Threads))
	for _, summary := range all.Threads {
		threads = append(threads, toProtoNeedsMeThreadSummary(summary))
	}
	slices.SortFunc(threads, func(a, b *ariadnev1.NeedsMeThreadSummary) int {
		return cmp.Compare(a.Id, b.Id)
	})
	return connect.NewResponse(&ariadnev1.ListAllNeedsMeResponse{
		Items:   items,
		Threads: threads,
	}), nil
}

func (s *projectService) GetProject(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetProjectRequest],
) (*connect.Response[ariadnev1.GetProjectResponse], error) {
	id := req.Msg.ProjectId
	repo := s.api.options.Repository
	detail, delta, err := s.api.syncer.ProjectDetail(ctx, repo.Reader, id, req.Msg.Since)
	if err != nil {
		return nil, err
	}
	threads := make([]*ariadnev1.ProjectThreadView, 0, len(delta.Threads))
	for _, thread := range delta.Threads {
		threads = append(threads, toProtoProjectThreadView(thread))
	}
	needsMe := make([]*ariadnev1.NeedsMeItem, 0, len(detail.NeedsMe))
	for _, item := range detail.NeedsMe {
		if converted := toProtoNeedsMeItem(item); converted != nil {
			needsMe = append(needsMe, converted)
		}
	}
	syncMsg := &ariadnev1.ProjectThreadSync{
		Cursor:  delta.Cursor,
		Partial: delta.Partial,
	}
	if delta.IDs != nil {
		idsCopy := make([]string, len(*delta.IDs))
		copy(idsCopy, *delta.IDs)
		syncMsg.Ids = &ariadnev1.ProjectStringList{Values: idsCopy}
	}
	hasTracker := s.api.options.HasTracker != nil && s.api.options.HasTracker(id)
	return connect.NewResponse(&ariadnev1.GetProjectResponse{
		Project:    toProtoProjectView(detail.Project),
		Threads:    threads,
		NeedsMe:    needsMe,
		Sync:       syncMsg,
		HasTracker: hasTracker,
	}), nil
}

func (s *projectService) CreateProject(
	ctx context.Context,
	req *connect.Request[ariadnev1.CreateProjectRequest],
) (*connect.Response[ariadnev1.CreateProjectResponse], error) {
	msg := req.Msg
	var name *string
	if msg.Name != nil {
		value := *msg.Name
		name = &value
	}
	if msg.HasSources || len(msg.Sources) > 0 {
		if msg.Source != nil || len(msg.References) > 0 {
			return nil, badInput("sources")
		}
		body := object{"sources": fromProtoFileSourceInputs(msg.Sources)}
		if msg.PrimaryIndex != nil {
			body["primaryIndex"] = float64(*msg.PrimaryIndex)
		}
		set, secrets, err := s.api.readFileSourceSet(ctx, body, core.ProjectView{})
		if err != nil {
			return nil, err
		}
		project, err := s.api.options.Repository.CreateProject(ctx, name, core.ProjectStart{FileSources: &set, Message: msg.Message})
		if err != nil {
			return nil, err
		}
		if s.api.options.Settings != nil && len(secrets) > 0 {
			if err := s.api.options.Settings.SaveFileSourceSecrets(project.ID, nil, secrets); err != nil {
				return nil, err
			}
		}
		return connect.NewResponse(&ariadnev1.CreateProjectResponse{
			Project: toProtoProjectView(project),
		}), nil
	}

	var source *sources.Definition
	secrets := object{}
	if msg.Source != nil {
		value, readSecrets, err := s.api.readSource(ctx, fromProtoProjectSource(msg.Source), "", nil, "", false)
		if err != nil {
			return nil, err
		}
		source = &value
		secrets = readSecrets
	}
	checked := []checkedReference{}
	references := []core.NewReference{}
	for index, rawRef := range msg.References {
		part, err := s.api.readSourcePart(fromProtoProjectSourcePart(rawRef), "references["+strconv.Itoa(index)+"]")
		if err != nil {
			return nil, err
		}
		reference, err := s.api.checkReference(ctx, "", source, secrets, part)
		if err != nil {
			return nil, err
		}
		checked = append(checked, reference)
		references = append(references, core.NewReference{
			Source: core.Source{Kind: reference.part.Kind, Settings: reference.part.Settings},
			Where:  reference.where,
		})
	}
	var rawSource any
	if source != nil {
		var err error
		rawSource, err = sourceObject(*source)
		if err != nil {
			return nil, err
		}
	}
	project, err := s.api.options.Repository.CreateProject(ctx, name, core.ProjectStart{
		Source:     rawSource,
		Message:    msg.Message,
		References: references,
	})
	if err != nil {
		return nil, err
	}
	if store := s.api.options.Settings; store != nil {
		if source != nil {
			if err := store.SaveSourceSecrets(project.ID, secrets); err != nil {
				return nil, err
			}
		}
		data, _ := json.Marshal(project.References)
		var refs []core.Reference
		if err := json.Unmarshal(data, &refs); err != nil {
			return nil, err
		}
		for index, reference := range checked {
			if len(reference.secrets) > 0 && index < len(refs) {
				if err := store.SaveReferenceSecrets(project.ID, refs[index].ID, reference.secrets); err != nil {
					return nil, err
				}
			}
		}
	}
	return connect.NewResponse(&ariadnev1.CreateProjectResponse{
		Project: toProtoProjectView(project),
	}), nil
}

func (s *projectService) UpdateProject(
	ctx context.Context,
	req *connect.Request[ariadnev1.UpdateProjectRequest],
) (*connect.Response[ariadnev1.UpdateProjectResponse], error) {
	msg := req.Msg
	id := msg.ProjectId
	repo := s.api.options.Repository
	current, err := repo.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}

	patch := core.ProjectPatch{}
	if msg.Name != nil {
		value := *msg.Name
		if projectsettings.Trim(value) == "" {
			return nil, badInput("name")
		}
		patch.Name = &value
	}

	autopilotEnabled := current.Settings.Autopilot.Enabled
	if msg.Settings != nil {
		rawSettings := fromProtoProjectSettings(msg.Settings)
		parsed, err := projectsettings.Parse(rawSettings, projectToolSwitches)
		if err != nil {
			return nil, err
		}
		if err := s.validateProjectModelOverrides(parsed.Models, current.Settings.Models); err != nil {
			return nil, err
		}
		autopilotEnabled = parsed.Autopilot.Enabled
		patch.Settings = rawSettings
		patch.HasSettings = true
	}

	switch action := msg.SlotsAction.(type) {
	case *ariadnev1.UpdateProjectRequest_SetSlots:
		value := int(action.SetSlots)
		if value < 1 || value > 16 {
			return nil, badInput("slots")
		}
		patch.Slots = &value
		patch.HasSlots = true
	case *ariadnev1.UpdateProjectRequest_ClearSlots:
		if action.ClearSlots {
			patch.Slots = nil
			patch.HasSlots = true
		}
	}

	if msg.McpServers != nil {
		known := map[string]bool{}
		if s.api.options.Settings != nil {
			servers, err := s.api.options.Settings.MCPServers()
			if err != nil {
				return nil, err
			}
			for _, server := range servers {
				known[server.ID] = true
			}
		}
		ids := make([]string, 0, len(msg.McpServers.Values))
		for _, serverID := range msg.McpServers.Values {
			if !known[serverID] {
				return nil, badInput("servers")
			}
			ids = append(ids, serverID)
		}
		patch.MCP = ids
		patch.HasMCP = true
	}

	if msg.AutopilotPaused != nil {
		if !autopilotEnabled {
			return nil, domainerr.New("error.autopilotOff", nil)
		}
		value := *msg.AutopilotPaused
		patch.AutopilotPaused = &value
	}

	env := []string{}
	if s.api.options.Settings != nil {
		if msg.Env != nil {
			entries := make([]any, 0, len(msg.Env.Entries))
			for _, entry := range msg.Env.Entries {
				entries = append(entries, object{"name": entry.Name, "value": entry.Value})
			}
			env, err = s.api.options.Settings.UpdateProjectEnv(id, entries)
			if err != nil {
				return nil, err
			}
		} else {
			env = s.api.options.Settings.ProjectEnvNames(id)
		}
	}

	project, err := repo.UpdateProject(ctx, id, patch, projectToolSwitches)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.UpdateProjectResponse{
		Project:      toProtoProjectView(project),
		SettingsView: s.buildProjectSettingsView(ctx, project, env),
	}), nil
}

func (s *projectService) validateProjectModelOverrides(next, current projectsettings.Models) error {
	if s.api.options.Settings == nil {
		return nil
	}
	var kinds []sdk.ModelProviderInfo
	if s.api.options.Models != nil {
		kinds = s.api.options.Models.Kinds()
	}
	var known map[string]bool
	ensureKnown := func() map[string]bool {
		if known != nil {
			return known
		}
		known = map[string]bool{}
		for _, raw := range s.api.options.Settings.ModelChoices(kinds) {
			if choice := protoMap(raw); choice != nil {
				if id, ok := choice["id"].(string); ok && id != "" {
					known[id] = true
				}
			}
		}
		return known
	}
	checkRole := func(name string, nextRole, curRole *projectsettings.ModelRole) error {
		if nextRole == nil || nextRole.Model == nil || *nextRole.Model == "" {
			return nil
		}
		id := *nextRole.Model
		if curRole != nil && curRole.Model != nil && *curRole.Model == id {
			return nil
		}
		if !ensureKnown()[id] {
			return badInput("models." + name + ".model")
		}
		return nil
	}
	if err := checkRole("thread", next.Thread, current.Thread); err != nil {
		return err
	}
	if err := checkRole("coordinator", next.Coordinator, current.Coordinator); err != nil {
		return err
	}
	if err := checkRole("research", next.Research, current.Research); err != nil {
		return err
	}
	return nil
}

func (s *projectService) DeleteProject(
	ctx context.Context,
	req *connect.Request[ariadnev1.DeleteProjectRequest],
) (*connect.Response[ariadnev1.DeleteProjectResponse], error) {
	if err := s.api.deleteProjectQueued(ctx, req.Msg.ProjectId); err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.DeleteProjectResponse{}), nil
}

func (s *projectService) GetProjectActivity(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetProjectActivityRequest],
) (*connect.Response[ariadnev1.GetProjectActivityResponse], error) {
	activity, err := s.api.options.Repository.ProjectActivity(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, err
	}
	threads := make([]*ariadnev1.ProjectThreadActivity, 0, len(activity.Threads))
	for _, thread := range activity.Threads {
		threads = append(threads, &ariadnev1.ProjectThreadActivity{
			Id:       thread.ID,
			Title:    thread.Title,
			Status:   thread.Status,
			Progress: toProtoProjectProgress(thread.Progress),
		})
	}
	return connect.NewResponse(&ariadnev1.GetProjectActivityResponse{
		Coordinator: &ariadnev1.ProjectConversationActivity{
			Status:   activity.Coordinator.Status,
			Progress: toProtoProjectProgress(activity.Coordinator.Progress),
		},
		Threads: threads,
	}), nil
}

func (s *projectService) SearchProject(
	ctx context.Context,
	req *connect.Request[ariadnev1.SearchProjectRequest],
) (*connect.Response[ariadnev1.SearchProjectResponse], error) {
	prefer := ""
	if req.Msg.PreferProjectId != nil {
		prefer = *req.Msg.PreferProjectId
	}
	value, err := s.api.options.Repository.Search(ctx, req.Msg.ProjectId, prefix(req.Msg.Query, 200), prefer)
	if err != nil {
		return nil, err
	}
	resp := &ariadnev1.SearchProjectResponse{
		Terms:     anyStringSlice(value["terms"]),
		Threads:   []*ariadnev1.ProjectThreadSearchHit{},
		Artifacts: []*ariadnev1.ProjectArtifactSearchHit{},
		Messages:  []*ariadnev1.ProjectMessageSearchHit{},
	}
	if hits := asMapSlice(value["threads"]); len(hits) > 0 {
		for _, hit := range hits {
			resp.Threads = append(resp.Threads, &ariadnev1.ProjectThreadSearchHit{
				Id:          anyString(hit["id"]),
				Title:       anyString(hit["title"]),
				Snippet:     anyString(hit["snippet"]),
				At:          anyInt64(hit["at"]),
				ProjectId:   anyString(hit["projectId"]),
				ProjectName: anyString(hit["projectName"]),
				Stage:       anyString(hit["stage"]),
			})
		}
	}
	if hits := asMapSlice(value["artifacts"]); len(hits) > 0 {
		for _, hit := range hits {
			resp.Artifacts = append(resp.Artifacts, &ariadnev1.ProjectArtifactSearchHit{
				Id:           anyString(hit["id"]),
				ArtifactKind: anyString(hit["artifactKind"]),
				Title:        anyString(hit["title"]),
				Snippet:      anyString(hit["snippet"]),
				At:           anyInt64(hit["at"]),
				ProjectId:    anyString(hit["projectId"]),
				ProjectName:  anyString(hit["projectName"]),
			})
		}
	}
	if hits := asMapSlice(value["messages"]); len(hits) > 0 {
		for _, hit := range hits {
			resp.Messages = append(resp.Messages, &ariadnev1.ProjectMessageSearchHit{
				Id:             anyString(hit["id"]),
				ConversationId: anyString(hit["conversationId"]),
				Where:          anyStringPtr(hit["where"]),
				Snippet:        anyString(hit["snippet"]),
				At:             anyInt64(hit["at"]),
				ProjectId:      anyString(hit["projectId"]),
				ProjectName:    anyString(hit["projectName"]),
			})
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *projectService) GetProjectSettings(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetProjectSettingsRequest],
) (*connect.Response[ariadnev1.GetProjectSettingsResponse], error) {
	id := req.Msg.ProjectId
	project, err := s.api.options.Repository.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	env := []string{}
	if s.api.options.Settings != nil {
		env = s.api.options.Settings.ProjectEnvNames(id)
	}
	return connect.NewResponse(&ariadnev1.GetProjectSettingsResponse{
		SettingsView: s.buildProjectSettingsView(ctx, project, env),
	}), nil
}

func (s *projectService) buildProjectSettingsView(ctx context.Context, project core.ProjectView, env []string) *ariadnev1.ProjectSettingsView {
	hostedMerge := s.api.options.Workspaces != nil && s.api.options.Workspaces.HasRepo(ctx, project.ID)
	tools := make([]string, len(projectToolSwitches))
	copy(tools, projectToolSwitches)
	return &ariadnev1.ProjectSettingsView{
		Settings:    toProtoProjectSettings(project.Settings),
		Detected:    toProtoProjectDetected(project.Detected),
		Env:         env,
		Tools:       tools,
		HostedMerge: hostedMerge,
	}
}

func (s *projectService) GetFileSources(
	ctx context.Context,
	req *connect.Request[ariadnev1.GetFileSourcesRequest],
) (*connect.Response[ariadnev1.GetFileSourcesResponse], error) {
	id := req.Msg.ProjectId
	project, err := s.api.options.Repository.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	set := project.WritableSources()
	known := map[string]*ariadnev1.ProjectStringList{}
	if s.api.options.Settings != nil {
		all := s.api.options.Settings.SourceSecrets(id)
		for _, source := range set.Sources {
			known[source.ID] = &ariadnev1.ProjectStringList{Values: appendSecretNames(sources.FileSourceSecrets(all, source.ID))}
		}
		if refs, _ := project.References.([]any); len(refs) > 0 {
			for _, raw := range refs {
				reference, _ := raw.(map[string]any)
				refID := text(reference, "id")
				known[refID] = &ariadnev1.ProjectStringList{Values: appendSecretNames(sources.FileSourceSecrets(all, refID))}
			}
		}
	}
	list := make([]*ariadnev1.FileSource, 0, len(set.Sources))
	for _, source := range set.Sources {
		list = append(list, toProtoFileSource(source))
	}
	return connect.NewResponse(&ariadnev1.GetFileSourcesResponse{
		Sources:    list,
		PrimaryId:  set.PrimaryID,
		SecretsSet: known,
	}), nil
}

func (s *projectService) SetFileSources(
	ctx context.Context,
	req *connect.Request[ariadnev1.SetFileSourcesRequest],
) (*connect.Response[ariadnev1.SetFileSourcesResponse], error) {
	id := req.Msg.ProjectId
	before, err := s.api.options.Repository.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	body := object{"sources": fromProtoFileSourceInputs(req.Msg.Sources)}
	if req.Msg.PrimaryIndex != nil {
		body["primaryIndex"] = float64(*req.Msg.PrimaryIndex)
	}
	set, secrets, err := s.api.readFileSourceSet(ctx, body, before)
	if err != nil {
		return nil, err
	}
	old := before.WritableSources()
	previousSecrets := object{}
	if s.api.options.Settings != nil {
		previousSecrets = s.api.options.Settings.SourceSecrets(id)
	}
	project, err := s.api.options.Repository.SetProjectFileSources(ctx, id, old, set)
	if err != nil {
		return nil, err
	}
	if s.api.options.Settings != nil {
		touched := make([]string, 0, len(old.Sources)+len(set.Sources))
		for _, source := range old.Sources {
			touched = append(touched, source.ID)
		}
		for _, source := range set.Sources {
			touched = append(touched, source.ID)
		}
		if len(touched) > 0 {
			if err := s.api.options.Settings.SaveFileSourceSecrets(id, touched, secrets); err != nil {
				return nil, err
			}
		}
	}
	if s.api.options.Workspaces != nil {
		for _, previous := range old.Sources {
			current, found := set.Get(previous.ID)
			if !found || !reflect.DeepEqual(previous, current) ||
				!reflect.DeepEqual(sources.FileSourceSecrets(previousSecrets, previous.ID), sources.FileSourceSecrets(secrets, previous.ID)) {
				s.api.options.Workspaces.InvalidateFileSource(id, previous.ID)
			}
		}
	}
	return connect.NewResponse(&ariadnev1.SetFileSourcesResponse{
		Project: toProtoProjectView(project),
	}), nil
}

func (s *projectService) AddProjectReference(
	ctx context.Context,
	req *connect.Request[ariadnev1.AddProjectReferenceRequest],
) (*connect.Response[ariadnev1.AddProjectReferenceResponse], error) {
	if req.Msg.Reference == nil {
		return nil, badInput("kind")
	}
	part, err := s.api.readSourcePart(fromProtoProjectSourcePart(req.Msg.Reference), "source")
	if err != nil {
		return nil, err
	}
	id := req.Msg.ProjectId
	project, err := s.api.options.Repository.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	source, err := sources.DecodeDefinition(project.Source)
	if err != nil {
		return nil, err
	}
	secrets := object{}
	if store := s.api.options.Settings; store != nil {
		secrets = sources.ReferenceSecretInputs(store.SourceSecrets(id), project.WritableSources().PrimaryID)
	}
	reference, err := s.api.checkReference(ctx, id, source, secrets, part)
	if err != nil {
		return nil, err
	}
	added, err := s.api.options.Repository.AddProjectReference(ctx, id, core.Source{
		Kind:     reference.part.Kind,
		Settings: reference.part.Settings,
	}, reference.where)
	if err != nil {
		return nil, err
	}
	if store := s.api.options.Settings; store != nil && len(reference.secrets) > 0 {
		if err := store.SaveReferenceSecrets(id, added.ID, reference.secrets); err != nil {
			return nil, err
		}
	}
	project, err = s.api.options.Repository.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&ariadnev1.AddProjectReferenceResponse{
		Project: toProtoProjectView(project),
	}), nil
}

func (s *projectService) RemoveProjectReference(
	ctx context.Context,
	req *connect.Request[ariadnev1.RemoveProjectReferenceRequest],
) (*connect.Response[ariadnev1.RemoveProjectReferenceResponse], error) {
	id, ref := req.Msg.ProjectId, req.Msg.ReferenceId
	project, err := s.api.options.Repository.RemoveProjectReference(ctx, id, ref)
	if err != nil {
		return nil, err
	}
	if s.api.options.Workspaces != nil {
		if err := s.api.options.Workspaces.InvalidateReference(ctx, id, ref); err != nil {
			return nil, err
		}
	}
	if store := s.api.options.Settings; store != nil {
		if err := store.SaveReferenceSecrets(id, ref, object{}); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&ariadnev1.RemoveProjectReferenceResponse{
		Project: toProtoProjectView(project),
	}), nil
}

// ---- Proto conversion helpers ----

func asMap(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	v := reflect.ValueOf(raw)
	if v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String {
		if v.IsNil() {
			return nil
		}
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = iter.Value().Interface()
		}
		return out
	}
	return nil
}

func asMapSlice(raw any) []map[string]any {
	if raw == nil {
		return nil
	}
	switch list := raw.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m := asMap(item); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	v := reflect.ValueOf(raw)
	if v.Kind() == reflect.Slice {
		out := make([]map[string]any, 0, v.Len())
		for i := range v.Len() {
			if m := asMap(v.Index(i).Interface()); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func toProtoStruct(raw any) *structpb.Struct {
	m := asMap(raw)
	if m == nil {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	normalized := normalizeStructMap(m)
	st, err := structpb.NewStruct(normalized)
	if err != nil {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	return st
}

func normalizeStructMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = normalizeStructValue(value)
	}
	return out
}

func normalizeStructValue(value any) any {
	switch v := value.(type) {
	case nil, bool, string, float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case uint:
		return float64(v)
	case uint32:
		return float64(v)
	case uint64:
		return float64(v)
	case map[string]any:
		return normalizeStructMap(v)
	case map[string]string:
		out := make(map[string]any, len(v))
		for k, s := range v {
			out[k] = s
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalizeStructValue(item)
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = item
		}
		return out
	default:
		if m := asMap(v); m != nil {
			return normalizeStructMap(m)
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil
		}
		return decoded
	}
}

func anyString(raw any) string {
	v, _ := raw.(string)
	return v
}

func anyStringPtr(raw any) *string {
	v, ok := raw.(string)
	if !ok {
		return nil
	}
	return &v
}

func anyBool(raw any) bool {
	v, _ := raw.(bool)
	return v
}

func anyInt64(raw any) int64 {
	switch v := raw.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func anyInt32Ptr(raw any) *int32 {
	if raw == nil {
		return nil
	}
	v := int32(anyInt64(raw))
	return &v
}

func anyFloat64(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func anyFloat64Ptr(raw any) *float64 {
	if raw == nil {
		return nil
	}
	v := anyFloat64(raw)
	return &v
}

func anyStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}

func toProtoUsageTotals(raw map[string]any) *ariadnev1.ProjectUsageTotals {
	if raw == nil {
		return &ariadnev1.ProjectUsageTotals{Cost: map[string]float64{}}
	}
	cost := map[string]float64{}
	switch c := raw["cost"].(type) {
	case map[string]float64:
		for k, v := range c {
			cost[k] = v
		}
	case map[string]any:
		for k, v := range c {
			cost[k] = anyFloat64(v)
		}
	}
	return &ariadnev1.ProjectUsageTotals{
		Input:      anyInt64(raw["input"]),
		Output:     anyInt64(raw["output"]),
		Cached:     anyInt64(raw["cached"]),
		CacheWrite: anyInt64(raw["cacheWrite"]),
		Reasoning:  anyInt64(raw["reasoning"]),
		Cost:       cost,
	}
}

func toProtoProjectSourcePart(raw any) *ariadnev1.ProjectSourcePart {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	return &ariadnev1.ProjectSourcePart{
		Kind:     anyString(m["kind"]),
		Settings: toProtoStruct(m["settings"]),
	}
}

func toProtoProjectSource(raw any) *ariadnev1.ProjectSource {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	return &ariadnev1.ProjectSource{
		Kind:     anyString(m["kind"]),
		Settings: toProtoStruct(m["settings"]),
		Hosting:  toProtoProjectSourcePart(m["hosting"]),
	}
}

func toProtoFileSource(source core.FileSource) *ariadnev1.FileSource {
	return &ariadnev1.FileSource{
		Id:       source.ID,
		Name:     source.Name,
		Kind:     source.Kind,
		Settings: toProtoStruct(source.Settings),
		Hosting:  toProtoProjectSourcePart(source.Hosting),
	}
}

func toProtoFileSourceSet(set *core.FileSourceSet) *ariadnev1.FileSourceSet {
	if set == nil {
		return nil
	}
	list := make([]*ariadnev1.FileSource, 0, len(set.Sources))
	for _, source := range set.Sources {
		list = append(list, toProtoFileSource(source))
	}
	return &ariadnev1.FileSourceSet{
		PrimaryId: set.PrimaryID,
		Sources:   list,
	}
}

func toProtoProjectSettings(settings projectsettings.Settings) *ariadnev1.ProjectSettings {
	routines := make([]*ariadnev1.ProjectRoutine, 0, len(settings.Routines))
	for _, routine := range settings.Routines {
		days := make([]int32, 0, len(routine.Days))
		for _, day := range routine.Days {
			days = append(days, int32(day))
		}
		routines = append(routines, &ariadnev1.ProjectRoutine{
			Id:      routine.ID,
			Prompt:  routine.Prompt,
			Days:    days,
			Time:    routine.Time,
			Enabled: routine.Enabled,
		})
	}
	tools := make([]string, len(settings.Tools))
	copy(tools, settings.Tools)
	out := &ariadnev1.ProjectSettings{
		Instructions: settings.Instructions,
		Delivery: &ariadnev1.ProjectDeliverySettings{
			Mode:         settings.Delivery.Mode,
			Instructions: settings.Delivery.Instructions,
		},
		Tools: tools,
		Workspace: &ariadnev1.ProjectWorkspaceSettings{
			Retention: settings.Workspace.Retention,
			Auto: &ariadnev1.ProjectWorkspaceAutoSettings{
				Setup: settings.Workspace.Auto.Setup,
				Check: settings.Workspace.Auto.Check,
			},
			Setup:        settings.Workspace.Setup,
			Shell:        settings.Workspace.Shell,
			Check:        settings.Workspace.Check,
			BranchPrefix: settings.Workspace.BranchPrefix,
		},
		Routines: routines,
		Review: &ariadnev1.ProjectReviewSettings{
			Enabled: settings.Review.Enabled,
		},
		Autopilot: &ariadnev1.ProjectAutopilotSettings{
			Enabled: settings.Autopilot.Enabled,
			Method:  settings.Autopilot.Method,
		},
	}
	if !settings.Models.IsZero() {
		out.Models = &ariadnev1.ProjectModelSettings{
			Thread:      toProtoProjectModelRole(settings.Models.Thread),
			Coordinator: toProtoProjectModelRole(settings.Models.Coordinator),
			Research:    toProtoProjectModelRole(settings.Models.Research),
		}
	}
	return out
}

func toProtoProjectModelRole(role *projectsettings.ModelRole) *ariadnev1.ProjectModelRoleSettings {
	if role == nil {
		return nil
	}
	out := &ariadnev1.ProjectModelRoleSettings{}
	if role.Model != nil {
		v := *role.Model
		out.Model = &v
	}
	if role.Effort != nil {
		v := *role.Effort
		out.Effort = &v
	}
	return out
}

func fromProtoProjectModelRole(role *ariadnev1.ProjectModelRoleSettings) object {
	entry := object{"model": nil, "effort": nil}
	if role.Model != nil {
		entry["model"] = *role.Model
	}
	if role.Effort != nil {
		entry["effort"] = *role.Effort
	}
	return entry
}

func fromProtoProjectSettings(settings *ariadnev1.ProjectSettings) object {
	out := object{
		"instructions": settings.Instructions,
	}
	if settings.Delivery != nil {
		out["delivery"] = object{
			"mode":         settings.Delivery.Mode,
			"instructions": settings.Delivery.Instructions,
		}
	}
	tools := make([]any, 0, len(settings.Tools))
	for _, tool := range settings.Tools {
		tools = append(tools, tool)
	}
	out["tools"] = tools
	if settings.Workspace != nil {
		ws := object{
			"retention":    settings.Workspace.Retention,
			"setup":        settings.Workspace.Setup,
			"shell":        settings.Workspace.Shell,
			"check":        settings.Workspace.Check,
			"branchPrefix": settings.Workspace.BranchPrefix,
		}
		if settings.Workspace.Auto != nil {
			ws["auto"] = object{
				"setup": settings.Workspace.Auto.Setup,
				"check": settings.Workspace.Auto.Check,
			}
		}
		out["workspace"] = ws
	}
	routines := make([]any, 0, len(settings.Routines))
	for _, routine := range settings.Routines {
		days := make([]any, 0, len(routine.Days))
		for _, day := range routine.Days {
			days = append(days, float64(day))
		}
		routines = append(routines, object{
			"id":      routine.Id,
			"prompt":  routine.Prompt,
			"days":    days,
			"time":    routine.Time,
			"enabled": routine.Enabled,
		})
	}
	out["routines"] = routines
	if settings.Review != nil {
		out["review"] = object{"enabled": settings.Review.Enabled}
	}
	if settings.Autopilot != nil {
		out["autopilot"] = object{
			"enabled": settings.Autopilot.Enabled,
			"method":  settings.Autopilot.Method,
		}
	}
	if settings.Models != nil {
		modelsObj := object{}
		if settings.Models.Thread != nil {
			modelsObj["thread"] = fromProtoProjectModelRole(settings.Models.Thread)
		}
		if settings.Models.Coordinator != nil {
			modelsObj["coordinator"] = fromProtoProjectModelRole(settings.Models.Coordinator)
		}
		if settings.Models.Research != nil {
			modelsObj["research"] = fromProtoProjectModelRole(settings.Models.Research)
		}
		out["models"] = modelsObj
	}
	return out
}

func toProtoProjectDetected(raw any) *ariadnev1.ProjectDetected {
	out := &ariadnev1.ProjectDetected{}
	m := asMap(raw)
	if m == nil {
		return out
	}
	if setup := asMap(m["setup"]); setup != nil {
		out.Setup = &ariadnev1.ProjectDetectedCommand{
			Command:  anyString(setup["command"]),
			ThreadId: anyString(setup["threadId"]),
		}
	}
	if check := asMap(m["check"]); check != nil {
		out.Check = &ariadnev1.ProjectDetectedCommand{
			Command:  anyString(check["command"]),
			ThreadId: anyString(check["threadId"]),
		}
	}
	return out
}

func toProtoProjectView(project core.ProjectView) *ariadnev1.ProjectView {
	var runningTurns *int32
	if project.RunningTurns > 0 {
		v := int32(project.RunningTurns)
		runningTurns = &v
	}
	refs := []*ariadnev1.ProjectReference{}
	for _, m := range asMapSlice(project.References) {
		refs = append(refs, &ariadnev1.ProjectReference{
			Id:       anyString(m["id"]),
			Name:     anyString(m["name"]),
			Kind:     anyString(m["kind"]),
			Settings: toProtoStruct(m["settings"]),
		})
	}
	return &ariadnev1.ProjectView{
		Id:              project.ID,
		Name:            project.Name,
		Unnamed:         project.Unnamed,
		CoordinatorId:   project.CoordinatorID,
		CreatedAt:       project.CreatedAt,
		UpdatedAt:       project.UpdatedAt,
		NeedsMe:         int32(project.NeedsMe),
		RunningTurns:    runningTurns,
		Source:          toProtoProjectSource(project.Source),
		FileSources:     toProtoFileSourceSet(project.FileSources),
		References:      refs,
		Slots:           anyInt32Ptr(project.Slots),
		Mcp:             anyStringSlice(project.MCP),
		Settings:        toProtoProjectSettings(project.Settings),
		Detected:        toProtoProjectDetected(project.Detected),
		AutopilotPaused: project.AutopilotPaused,
		Usage:           toProtoUsageTotals(project.Usage),
	}
}

func toProtoProjectProgress(progress *core.Progress) *ariadnev1.ProjectProgress {
	if progress == nil {
		return nil
	}
	return &ariadnev1.ProjectProgress{
		Done:  int32(progress.Done),
		Total: int32(progress.Total),
	}
}

func toProtoProjectThreadView(thread core.ThreadView) *ariadnev1.ProjectThreadView {
	out := &ariadnev1.ProjectThreadView{
		Id:            thread.ID,
		ProjectId:     thread.ProjectID,
		Title:         thread.Title,
		NodeId:        anyStringPtr(thread.NodeID),
		Stage:         thread.Stage,
		LastReport:    anyStringPtr(thread.LastReport),
		LastSummary:   anyStringPtr(thread.LastSummary),
		LastHeadline:  anyStringPtr(thread.LastHeadline),
		Archived:      thread.Archived,
		Model:         anyStringPtr(thread.Model),
		Effort:        anyStringPtr(thread.Effort),
		BranchSlug:    anyStringPtr(thread.BranchSlug),
		Usage:         toProtoUsageTotals(thread.Usage),
		Progress:      toProtoProjectProgress(thread.Progress),
		Doing:         anyStringPtr(thread.Doing),
		CreatedAt:     thread.CreatedAt,
		UpdatedAt:     thread.UpdatedAt,
		ActiveAt:      thread.ActiveAt,
		ReviewOf:      anyStringPtr(thread.ReviewOf),
		Auto:          thread.Auto,
		AutopilotHeld: anyStringPtr(thread.AutopilotHeld),
	}
	for _, m := range asMapSlice(thread.LastArtifacts) {
		out.LastArtifacts = append(out.LastArtifacts, &ariadnev1.ProjectThreadArtifact{
			Id:    anyString(m["id"]),
			Kind:  anyString(m["kind"]),
			Title: anyString(m["title"]),
		})
	}
	if thread.Workspace != nil {
		var sourceID *string
		if thread.Workspace.SourceID != "" {
			v := thread.Workspace.SourceID
			sourceID = &v
		}
		out.Workspace = &ariadnev1.ProjectWorkspaceView{
			SourceId: sourceID,
			Label:    thread.Workspace.Label,
			Branch:   anyStringPtr(thread.Workspace.Branch),
			Base:     anyStringPtr(thread.Workspace.Base),
			Removed:  thread.Workspace.Removed,
			Parked:   thread.Workspace.Parked,
		}
	}
	if m := asMap(thread.Merged); m != nil {
		out.Merged = &ariadnev1.ProjectBranchMerge{
			Branch: anyString(m["branch"]),
			Base:   anyString(m["base"]),
			Method: anyString(m["method"]),
			Sha:    anyString(m["sha"]),
			Head:   anyString(m["head"]),
			Auto:   anyBool(m["auto"]),
			At:     anyInt64(m["at"]),
		}
	}
	if m := asMap(thread.Changes); m != nil {
		out.Changes = &ariadnev1.ProjectChangeTotals{
			Files:   int32(anyInt64(m["files"])),
			Added:   int32(anyInt64(m["added"])),
			Removed: int32(anyInt64(m["removed"])),
		}
	}
	if m := asMap(thread.PullRequest); m != nil {
		checks := &ariadnev1.ProjectPullRequestChecks{}
		if c := asMap(m["checks"]); c != nil {
			checks.State = anyString(c["state"])
			checks.Failed = anyStringSlice(c["failed"])
		}
		out.PullRequest = &ariadnev1.ProjectThreadPullRequest{
			Number:     anyInt64(m["number"]),
			Reference:  anyStringPtr(m["reference"]),
			Url:        anyString(m["url"]),
			Title:      anyString(m["title"]),
			State:      anyString(m["state"]),
			Conflicted: anyBool(m["conflicted"]),
			HeadSha:    anyString(m["headSha"]),
			Checks:     checks,
		}
	}
	if thread.Aside != nil {
		out.Aside = &ariadnev1.ProjectAsideView{
			Until:  thread.Aside.Until,
			Reason: thread.Aside.Reason,
			By:     thread.Aside.By,
			Due:    thread.Aside.Due,
		}
	}
	if thread.Stopped != nil {
		out.Stopped = &ariadnev1.ProjectStoppedView{
			Reason: thread.Stopped.Reason,
			By:     thread.Stopped.By,
			At:     thread.Stopped.At,
		}
	}
	for _, m := range asMapSlice(thread.References) {
		out.References = append(out.References, &ariadnev1.ProjectThreadReference{
			Id:          anyString(m["id"]),
			Name:        anyString(m["name"]),
			Kind:        anyString(m["kind"]),
			Settings:    toProtoStruct(m["settings"]),
			Writable:    anyBool(m["writable"]),
			AttentionId: anyString(m["attentionId"]),
		})
	}
	for _, m := range asMapSlice(thread.EndedReferences) {
		out.EndedReferences = append(out.EndedReferences, &ariadnev1.ProjectEndedReference{
			AttentionId: anyString(m["attentionId"]),
			Kept:        anyBool(m["kept"]),
		})
	}
	if thread.Review != nil {
		out.Review = &ariadnev1.ProjectReviewView{
			ReviewerId: thread.Review.ReviewerID,
			Rounds:     thread.Review.Rounds,
			Reviewing:  thread.Review.Reviewing,
		}
	}
	return out
}

func toProtoNeedsMeThreadSummary(summary core.NeedsMeThreadSummary) *ariadnev1.NeedsMeThreadSummary {
	out := &ariadnev1.NeedsMeThreadSummary{
		Id:            summary.ID,
		ProjectId:     summary.ProjectID,
		Title:         summary.Title,
		Stage:         summary.Stage,
		LastHeadline:  anyStringPtr(summary.LastHeadline),
		AutopilotHeld: anyStringPtr(summary.AutopilotHeld),
	}
	for _, m := range asMapSlice(summary.LastArtifacts) {
		out.LastArtifacts = append(out.LastArtifacts, &ariadnev1.ProjectThreadArtifact{
			Id:    anyString(m["id"]),
			Kind:  anyString(m["kind"]),
			Title: anyString(m["title"]),
		})
	}
	if summary.Aside != nil {
		out.Aside = &ariadnev1.ProjectAsideView{
			Until:  summary.Aside.Until,
			Reason: summary.Aside.Reason,
			By:     summary.Aside.By,
			Due:    summary.Aside.Due,
		}
	}
	if summary.Review != nil {
		out.Review = &ariadnev1.ProjectReviewView{
			ReviewerId: summary.Review.ReviewerID,
			Rounds:     summary.Review.Rounds,
			Reviewing:  summary.Review.Reviewing,
		}
	}
	if summary.Stopped != nil {
		out.Stopped = &ariadnev1.ProjectStoppedView{
			Reason: summary.Stopped.Reason,
			By:     summary.Stopped.By,
			At:     summary.Stopped.At,
		}
	}
	return out
}

func toProtoProjectAskBlock(raw any) *ariadnev1.ProjectAskBlock {
	m := asMap(raw)
	if m == nil {
		return nil
	}
	switch anyString(m["type"]) {
	case "choice":
		options := []*ariadnev1.ProjectChoiceOption{}
		for _, opt := range asMapSlice(m["options"]) {
			options = append(options, &ariadnev1.ProjectChoiceOption{
				Label:       anyString(opt["label"]),
				Consequence: anyString(opt["consequence"]),
			})
		}
		return &ariadnev1.ProjectAskBlock{
			Block: &ariadnev1.ProjectAskBlock_Choice{
				Choice: &ariadnev1.ProjectChoiceBlock{
					Question:    anyString(m["question"]),
					Options:     options,
					Recommended: anyInt32Ptr(m["recommended"]),
				},
			},
		}
	case "confirm":
		return &ariadnev1.ProjectAskBlock{
			Block: &ariadnev1.ProjectAskBlock_Confirm{
				Confirm: &ariadnev1.ProjectConfirmBlock{
					Title:                 anyString(m["title"]),
					Description:           anyStringPtr(m["description"]),
					Detail:                anyStringPtr(m["detail"]),
					Tone:                  anyString(m["tone"]),
					ApproveText:           anyStringPtr(m["approveText"]),
					DenyText:              anyStringPtr(m["denyText"]),
					AllowProjectReference: anyBool(m["allowProjectReference"]),
				},
			},
		}
	case "form":
		fields := []*ariadnev1.ProjectFormField{}
		for _, f := range asMapSlice(m["fields"]) {
			options := []*ariadnev1.ProjectFormOption{}
			for _, opt := range asMapSlice(f["options"]) {
				options = append(options, &ariadnev1.ProjectFormOption{
					Label: anyString(opt["label"]),
					Value: anyString(opt["value"]),
				})
			}
			fields = append(fields, &ariadnev1.ProjectFormField{
				Key:          anyString(f["key"]),
				Label:        anyString(f["label"]),
				Kind:         anyString(f["kind"]),
				Required:     anyBool(f["required"]),
				Placeholder:  anyStringPtr(f["placeholder"]),
				Options:      options,
				DefaultValue: toProtoFormValue(f["default"]),
				Min:          anyFloat64Ptr(f["min"]),
				Max:          anyFloat64Ptr(f["max"]),
			})
		}
		return &ariadnev1.ProjectAskBlock{
			Block: &ariadnev1.ProjectAskBlock_Form{
				Form: &ariadnev1.ProjectFormBlock{
					Title:       anyString(m["title"]),
					Description: anyStringPtr(m["description"]),
					Fields:      fields,
					SubmitText:  anyStringPtr(m["submitText"]),
				},
			},
		}
	default:
		return nil
	}
}

func toProtoFormValue(raw any) *ariadnev1.ProjectFormValue {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_StringValue{StringValue: v}}
	case bool:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_BoolValue{BoolValue: v}}
	case float64:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_NumberValue{NumberValue: v}}
	case int:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_NumberValue{NumberValue: float64(v)}}
	case int64:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_NumberValue{NumberValue: float64(v)}}
	case []string:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_StringList{StringList: &ariadnev1.ProjectStringList{Values: v}}}
	case []any:
		return &ariadnev1.ProjectFormValue{Value: &ariadnev1.ProjectFormValue_StringList{StringList: &ariadnev1.ProjectStringList{Values: anyStringSlice(v)}}}
	default:
		return nil
	}
}

func toProtoNeedsMeItem(input any) *ariadnev1.NeedsMeItem {
	raw := asMap(input)
	if raw == nil {
		return nil
	}
	switch anyString(raw["kind"]) {
	case "attention":
		var att *ariadnev1.ProjectAttentionView
		switch value := raw["attention"].(type) {
		case core.AttentionView:
			att = &ariadnev1.ProjectAttentionView{
				Id:             value.ID,
				ProjectId:      value.ProjectID,
				ThreadId:       anyStringPtr(value.ThreadID),
				JoinedBy:       value.JoinedBy,
				Block:          toProtoProjectAskBlock(value.Block),
				MessageId:      anyStringPtr(value.MessageID),
				ConversationId: anyStringPtr(value.ConversationID),
				CreatedAt:      value.CreatedAt,
			}
		case *core.AttentionView:
			if value != nil {
				att = &ariadnev1.ProjectAttentionView{
					Id:             value.ID,
					ProjectId:      value.ProjectID,
					ThreadId:       anyStringPtr(value.ThreadID),
					JoinedBy:       value.JoinedBy,
					Block:          toProtoProjectAskBlock(value.Block),
					MessageId:      anyStringPtr(value.MessageID),
					ConversationId: anyStringPtr(value.ConversationID),
					CreatedAt:      value.CreatedAt,
				}
			}
		}
		return &ariadnev1.NeedsMeItem{
			Item: &ariadnev1.NeedsMeItem_Attention{
				Attention: &ariadnev1.NeedsMeAttention{
					Attention: att,
					ThreadId:  anyStringPtr(raw["threadId"]),
					JoinedIds: anyStringSlice(raw["joinedIds"]),
				},
			},
		}
	case "thread":
		return &ariadnev1.NeedsMeItem{
			Item: &ariadnev1.NeedsMeItem_Thread{
				Thread: &ariadnev1.NeedsMeThread{
					ThreadId: anyString(raw["threadId"]),
				},
			},
		}
	default:
		return nil
	}
}

func fromProtoProjectSourcePart(part *ariadnev1.ProjectSourcePart) object {
	if part == nil {
		return nil
	}
	settings := object{}
	if part.Settings != nil {
		settings = part.Settings.AsMap()
	}
	return object{
		"kind":     part.Kind,
		"settings": settings,
	}
}

func fromProtoProjectSource(source *ariadnev1.ProjectSource) object {
	if source == nil {
		return nil
	}
	settings := object{}
	if source.Settings != nil {
		settings = source.Settings.AsMap()
	}
	out := object{
		"kind":     source.Kind,
		"settings": settings,
	}
	if source.Hosting != nil {
		out["hosting"] = fromProtoProjectSourcePart(source.Hosting)
	}
	return out
}

func fromProtoFileSourceInputs(inputs []*ariadnev1.FileSourceInput) []any {
	out := make([]any, 0, len(inputs))
	for _, item := range inputs {
		settings := object{}
		if item.Settings != nil {
			settings = item.Settings.AsMap()
		}
		entry := object{
			"kind":     item.Kind,
			"settings": settings,
		}
		if item.Id != nil {
			entry["id"] = *item.Id
		}
		if item.Hosting != nil {
			entry["hosting"] = fromProtoProjectSourcePart(item.Hosting)
		}
		out = append(out, entry)
	}
	return out
}
