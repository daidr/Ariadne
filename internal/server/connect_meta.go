package server

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

type metaServiceHandler struct {
	api *API
}

func (api *API) registerMetaRPC() {
	serviceDesc := ariadnev1.File_ariadne_v1_meta_proto.Services().ByName("MetaService")
	handler := &metaServiceHandler{api: api}
	api.mountConnect(serviceDesc, func(opts ...connect.HandlerOption) (string, http.Handler) {
		return ariadnev1connect.NewMetaServiceHandler(handler, opts...)
	})
}

func (h *metaServiceHandler) Ping(
	_ context.Context,
	_ *connect.Request[ariadnev1.PingRequest],
) (*connect.Response[ariadnev1.PingResponse], error) {
	return connect.NewResponse(&ariadnev1.PingResponse{Ok: true}), nil
}

func (h *metaServiceHandler) ListSourceKinds(
	ctx context.Context,
	_ *connect.Request[ariadnev1.ListSourceKindsRequest],
) (*connect.Response[ariadnev1.ListSourceKindsResponse], error) {
	kinds := h.api.sourceKinds(ctx)
	out := make([]*ariadnev1.SourceKind, 0, len(kinds))
	for _, info := range kinds {
		item := &ariadnev1.SourceKind{
			Kind:     info.Kind,
			Title:    toProtoLocalizedText(info.Title),
			Settings: toProtoSettingsSchema(info.Settings),
			Provides: slices.Clone(info.Provides),
			Remote:   info.Remote,
			Hosting:  info.Hosting,
			Browse:   info.Browse,
			Summary:  info.Summary,
			Defaults: mapToStruct(info.Defaults),
		}
		if info.Plugin != nil {
			item.Plugin = &ariadnev1.SourcePluginInfo{
				Id:    info.Plugin.ID,
				Ready: info.Plugin.Ready,
			}
		}
		out = append(out, item)
	}
	return connect.NewResponse(&ariadnev1.ListSourceKindsResponse{Kinds: out}), nil
}

func (h *metaServiceHandler) BrowseSource(
	ctx context.Context,
	req *connect.Request[ariadnev1.BrowseSourceRequest],
) (*connect.Response[ariadnev1.BrowseSourceResponse], error) {
	kind := req.Msg.GetKind()
	if h.api.options.Sources == nil {
		return nil, domainerr.NotFound(kind)
	}
	provider, exists := h.api.options.Sources.Get(kind)
	if !exists {
		return nil, domainerr.NotFound(kind)
	}
	if provider.Browse == nil {
		return nil, badInput("kind")
	}
	given := structToMap(req.Msg.GetSettings())
	filled := maps.Clone(given)
	if projectID := req.Msg.GetProjectId(); projectID != "" && h.api.options.Settings != nil {
		project, err := h.api.options.Repository.GetProject(ctx, projectID)
		if err != nil {
			return nil, err
		}
		previous := ""
		prefix := ""
		set := project.WritableSources()
		sourceID := set.PrimaryID
		if req.Msg.SourceId != nil {
			sourceID = req.Msg.GetSourceId()
		}
		if source, ok := set.Get(sourceID); ok {
			previous = source.Kind
			prefix = sources.FileSourceSecretPrefix(source.ID)
		} else if sourceID != "" {
			if refs, _ := project.References.([]any); len(refs) > 0 {
				for _, raw := range refs {
					reference, _ := raw.(map[string]any)
					if text(reference, "id") == sourceID {
						previous = text(reference, "kind")
						prefix = sources.FileSourceSecretPrefix(sourceID)
					}
				}
			}
		}
		plain, secrets := h.api.options.Settings.SplitSourceSettings(projectID, kind, given, previous, prefix, sources.SecretFields(provider.Info))
		filled = plain
		for key, value := range secrets {
			filled[strings.TrimPrefix(key, prefix)] = value
		}
	}
	browseCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	listing, err := provider.Browse(browseCtx, filled, req.Msg.GetPath())
	var absent *sdk.DirectoryNotFound
	if errors.As(err, &absent) {
		return connect.NewResponse(&ariadnev1.BrowseSourceResponse{}), nil
	}
	if err != nil {
		var known *domainerr.Error
		if errors.As(err, &known) {
			return nil, err
		}
		return nil, domainerr.New("error.browse", object{"detail": err.Error()})
	}
	return connect.NewResponse(&ariadnev1.BrowseSourceResponse{
		Listing: toProtoDirectoryListing(listing),
	}), nil
}

func (h *metaServiceHandler) BrowseLocal(
	ctx context.Context,
	req *connect.Request[ariadnev1.BrowseLocalRequest],
) (*connect.Response[ariadnev1.BrowseLocalResponse], error) {
	browseCtx, cancel := context.WithTimeout(ctx, localBrowseTimeout)
	defer cancel()
	listing, err := local.BrowseLocal(browseCtx, req.Msg.GetPath())
	var absent *sdk.DirectoryNotFound
	if errors.As(err, &absent) {
		return connect.NewResponse(&ariadnev1.BrowseLocalResponse{}), nil
	}
	if err != nil {
		return nil, domainerr.New("error.browse", object{"detail": err.Error()})
	}
	return connect.NewResponse(&ariadnev1.BrowseLocalResponse{
		Listing: toProtoDirectoryListing(listing),
	}), nil
}

func (h *metaServiceHandler) SummarizeLinks(
	_ context.Context,
	req *connect.Request[ariadnev1.SummarizeLinksRequest],
) (*connect.Response[ariadnev1.SummarizeLinksResponse], error) {
	urls := req.Msg.GetUrls()
	if len(urls) > links.MaxLinks {
		return nil, badInput("urls")
	}
	summarized := h.api.links.Summarize(urls)
	out := make(map[string]*ariadnev1.LinkSummaryEntry, len(summarized))
	for address, match := range summarized {
		entry := &ariadnev1.LinkSummaryEntry{}
		if match != nil {
			summary := &ariadnev1.LinkSummary{
				Kind:  match.Kind,
				Label: match.Label,
			}
			if match.Reference != "" {
				ref := match.Reference
				summary.Reference = &ref
			}
			if match.RepositoryURL != "" {
				repoURL := match.RepositoryURL
				summary.RepositoryUrl = &repoURL
			}
			entry.Summary = summary
		}
		out[address] = entry
	}
	return connect.NewResponse(&ariadnev1.SummarizeLinksResponse{Links: out}), nil
}

func (h *metaServiceHandler) PreviewLink(
	ctx context.Context,
	req *connect.Request[ariadnev1.PreviewLinkRequest],
) (*connect.Response[ariadnev1.PreviewLinkResponse], error) {
	address := req.Msg.GetUrl()
	if address == "" {
		return nil, badInput("url")
	}
	preview := h.api.links.Preview(ctx, address)
	return connect.NewResponse(&ariadnev1.PreviewLinkResponse{
		Preview: toProtoLinkPreview(preview),
	}), nil
}

func toProtoDirectoryListing(listing *sdk.DirectoryListing) *ariadnev1.DirectoryListing {
	if listing == nil {
		return nil
	}
	dirs := make([]*ariadnev1.BrowsableDirectory, 0, len(listing.Directories))
	for _, dir := range listing.Directories {
		dirs = append(dirs, &ariadnev1.BrowsableDirectory{
			Name: dir.Name,
			Path: dir.Path,
		})
	}
	return &ariadnev1.DirectoryListing{
		Path:        listing.Path,
		Parent:      listing.Parent,
		Directories: dirs,
	}
}

func toProtoLinkPreview(preview *sdk.LinkPreview) *ariadnev1.LinkPreview {
	if preview == nil {
		return nil
	}
	out := &ariadnev1.LinkPreview{
		Kind:       preview.Kind,
		Repository: preview.Repository,
		Number:     int32(preview.Number),
		Url:        preview.URL,
		Title:      preview.Title,
		State:      preview.State,
		Draft:      preview.Draft,
		Conflicted: preview.Conflicted,
		Author:     preview.Author,
		At:         preview.At,
		Labels:     slices.Clone(preview.Labels),
	}
	if preview.Reference != "" {
		ref := preview.Reference
		out.Reference = &ref
	}
	if preview.Additions != nil {
		val := int32(*preview.Additions)
		out.Additions = &val
	}
	if preview.Deletions != nil {
		val := int32(*preview.Deletions)
		out.Deletions = &val
	}
	if preview.ChangedFiles != nil {
		val := int32(*preview.ChangedFiles)
		out.ChangedFiles = &val
	}
	if preview.UpdatedAt != "" {
		val := preview.UpdatedAt
		out.UpdatedAt = &val
	}
	if preview.StateReason != "" {
		val := preview.StateReason
		out.StateReason = &val
	}
	return out
}
