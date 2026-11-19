// Package server preserves the browser API while keeping transport separate from
// persistence and turn execution. Routes are added as their native services land.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/collab"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/dashboard"
	"github.com/daidr/Ariadne/internal/data"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/httpauth"
	"github.com/daidr/Ariadne/internal/links"
	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/modelcatalog"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/plugins"
	"github.com/daidr/Ariadne/internal/realtime"
	"github.com/daidr/Ariadne/internal/scheduler"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/internal/update"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

type Options struct {
	Repository       *core.Repository
	Token            string
	Now              func() time.Time
	Hub              *realtime.Hub
	Scheduler        *scheduler.Scheduler
	Web              fs.FS
	OnError          func(error)
	ModelOf          func(*http.Request, core.ConversationView) (any, error)
	HasTracker       func(string) bool
	Data             *data.Store
	Settings         *settings.Store
	Updates          *update.Service
	SearchAvailable  func() ([]string, []any)
	HTTPClient       *http.Client
	Models           *models.Registry
	Catalog          *modelcatalog.Catalog
	FindContext      *sdk.FindContext
	MCP              *mcp.Hub
	Sources          *sources.Registry
	Links            *links.Resolver
	State            *realtime.State
	Background       *background.Commands
	Workspaces       *workspaces.Manager
	OnProjectDeleted func(string)
	SkillsDirs       []string
	Dashboards       *dashboard.Registry
	SharedDashboard  dashboard.Shared
	Rooms            *collab.Rooms
	Plugins          *plugins.Host
}
type API struct {
	options       Options
	auth          httpauth.Auth
	files         files.Store
	syncer        *ThreadSync
	hub           *realtime.Hub
	ownedHub      bool
	mux           *http.ServeMux
	handler       http.Handler
	routes        []string
	closeOnce     sync.Once
	catalog       *modelcatalog.Catalog
	ownedCatalog  bool
	links         *links.Resolver
	ownedLinks    bool
	state         *realtime.State
	rooms         *collab.Rooms
	ownedState    bool
	deletionMu    sync.Mutex
	deletions     map[string]*projectDeletion
	deleteLife    context.Context
	deleteCancel  context.CancelFunc
	deleteJobs    sync.WaitGroup
	deleteClosed  bool
	deleteWaiters int
}
type object = map[string]any
type endpoint func(*http.Request) (any, int, error)

func New(options Options) (*API, error) {
	if options.Repository == nil {
		return nil, errors.New("API repository is required")
	}
	if options.Plugins != nil {
		if options.Settings == nil {
			return nil, errors.New("plugin management needs a settings store")
		}
		if err := options.Plugins.Restore(context.Background(), options.Settings); err != nil {
			return nil, err
		}
	}
	if options.Workspaces != nil {
		if options.Background == nil {
			options.Background = options.Workspaces.Background
		}
		if options.HasTracker == nil {
			options.HasTracker = func(id string) bool { return options.Workspaces.HasTracker(context.Background(), id) }
		}
	}
	if options.State != nil {
		if options.Hub != nil && options.Hub != options.State.Hub() {
			return nil, errors.New("API state and hub must share the same transient stream")
		}
		options.Hub = options.State.Hub()
	}
	api := &API{options: options, auth: httpauth.Auth{Token: options.Token, Now: options.Now}, files: files.Store{Events: options.Repository.Store, Now: options.Now}, syncer: NewThreadSync(), hub: options.Hub, mux: http.NewServeMux()}
	api.rooms = options.Rooms
	if api.rooms == nil {
		api.rooms = collab.NewRooms(options.Repository, api.report)
	}
	api.deleteLife, api.deleteCancel = context.WithCancel(context.Background())
	api.deletions = map[string]*projectDeletion{}
	api.links = options.Links
	if api.links == nil {
		api.links = links.New(links.Options{Now: options.Now})
		api.ownedLinks = true
	}
	api.catalog = options.Catalog
	if api.catalog == nil {
		api.catalog = modelcatalog.New(modelcatalog.Options{Client: options.HTTPClient, Now: options.Now})
		api.ownedCatalog = true
	}
	if api.hub == nil {
		api.hub = realtime.NewHub()
		api.ownedHub = true
	}
	api.state = options.State
	if api.state == nil {
		api.state = realtime.NewState(api.hub)
		api.ownedState = true
	}
	api.mux.HandleFunc("/", api.static)
	api.add("GET", "/api/health", func(*http.Request) (any, int, error) { return object{"ok": true}, 200, nil })
	api.registerReads()
	api.registerWrites()
	api.registerArtifacts()
	api.registerPlans()
	api.registerData()
	api.registerSettings()
	api.registerUpdates()
	api.registerPlugins()
	api.registerModels()
	api.registerModelCatalog()
	api.registerMCP()
	api.registerDirectories()
	api.registerSources()
	api.registerSourceConfig()
	api.registerLinks()
	api.registerBackground()
	api.registerWorkspaces()
	api.registerProjectDeletion()
	api.registerExport()
	api.registerTrace()
	api.registerSkills()
	api.registerConversationConfig()
	api.addRaw("GET", "/api/events", api.events)
	for _, method := range []string{"GET", "HEAD"} {
		api.addRaw(method, "/api/files/:fileId", func(w http.ResponseWriter, r *http.Request) error {
			if !fileID.MatchString(r.PathValue("fileId")) {
				return domainerr.NotFound(r.URL.Path)
			}
			api.files.Serve(w, r)
			return nil
		})
	}
	api.add("GET", "/api/files/:fileId/sheet", func(r *http.Request) (any, int, error) {
		id := r.PathValue("fileId")
		if !fileID.MatchString(id) {
			return nil, 0, domainerr.NotFound(id)
		}
		index := 0
		if given := r.URL.Query().Get("index"); given != "" {
			var err error
			index, err = strconv.Atoi(given)
			if err != nil || index < 0 {
				return nil, 0, badInput("invalid worksheet index")
			}
		}
		preview, err := api.files.PreviewSheet(r.Context(), id, index)
		return preview, 200, err
	})
	api.handler = api.auth.Middleware(api.mux)
	return api, nil
}
func (api *API) ServeHTTP(w http.ResponseWriter, r *http.Request) { api.handler.ServeHTTP(w, r) }
func (api *API) Close() {
	api.closeOnce.Do(func() {
		api.deletionMu.Lock()
		api.deleteClosed = true
		api.deleteCancel()
		api.deletionMu.Unlock()
		api.deleteJobs.Wait()
		api.rooms.Close()
		api.syncer.Close()
		if api.ownedState {
			api.state.Close()
		}
		if api.ownedLinks {
			api.links.Close()
		}
		if api.ownedHub {
			api.hub.Close()
		}
		if api.ownedCatalog {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = api.catalog.Close(ctx)
		}
	})
}
func badInput(reason string) error {
	return domainerr.New("error.invalidBody", object{"reason": reason})
}
func (api *API) report(err error) {
	if api.options.OnError != nil {
		api.options.OnError(err)
	}
}
func goPath(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "{" + part[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}
func (api *API) addRaw(method, pattern string, run func(http.ResponseWriter, *http.Request) error) {
	api.routes = append(api.routes, method+" "+pattern)
	api.mux.HandleFunc(method+" "+goPath(pattern), func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			domainerr.Write(w, domainerr.NotFound(r.URL.Path))
			return
		}
		if err := run(w, r); err != nil {
			var domain *domainerr.Error
			if !errors.As(err, &domain) {
				api.report(err)
			}
			domainerr.Write(w, err)
		}
	})
}
func (api *API) add(method, pattern string, run endpoint) {
	api.addRaw(method, pattern, func(w http.ResponseWriter, r *http.Request) error {
		value, status, err := run(r)
		if err != nil {
			return err
		}
		var data []byte
		if status != 204 {
			data, err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
		if pattern != "/api/health" {
			http.SetCookie(w, api.auth.FileSessionCookie(r))
			w.Header().Set("Cache-Control", "no-store")
		}
		if status == 0 {
			status = 200
		}
		if status != 204 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.WriteHeader(status)
		if status != 204 {
			_, err = w.Write(data)
		}
		return err
	})
}

// JSON is decoded directly from a bounded reader rather than copied through a
// second whole-body buffer. Inline whiteboard images fit within this limit.
func readJSON(request *http.Request) (object, error) {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 80*1024*1024+1))
	var value object
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, badInput("json")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, badInput("json")
	}
	return value, nil
}
func required(body object, key string) (string, error) {
	value, ok := body[key].(string)
	if !ok || textutil.Trim(value) == "" {
		return "", badInput(key)
	}
	return value, nil
}
func text(body object, key string) string { value, _ := body[key].(string); return value }
func prefix(text string, length int) string {
	units := utf16.Encode([]rune(text))
	return string(utf16.Decode(units[:min(length, len(units))]))
}
func (api *API) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || api.options.Web == nil {
		domainerr.Write(w, domainerr.NotFound(r.URL.Path))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(api.options.Web, name)
	if err != nil {
		name = "index.html"
		data, err = fs.ReadFile(api.options.Web, name)
	}
	if err != nil {
		api.report(err)
		domainerr.Write(w, domainerr.NotFound(r.URL.Path))
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != "HEAD" {
		_, _ = w.Write(data)
	}
}

var fileID = regexp.MustCompile(`^fil_[\w-]+$`)

func (api *API) events(w http.ResponseWriter, r *http.Request) error {
	given := r.URL.Query().Get("afterSeq")
	if _, exists := r.Header["Last-Event-Id"]; exists {
		given = r.Header.Get("Last-Event-ID")
	}
	after, err := api.options.Repository.Store.LastSeq(r.Context())
	if err != nil {
		return err
	}
	if _, exists := r.URL.Query()["afterSeq"]; exists || r.Header.Get("Last-Event-ID") != "" || len(r.Header.Values("Last-Event-ID")) > 0 {
		value, err := queryNumber(given)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.Trunc(value) != value || value > math.MaxInt64 {
			return badInput("afterSeq")
		}
		after = int64(value)
	}
	http.SetCookie(w, api.auth.FileSessionCookie(r))
	project := r.URL.Query().Get("projectId")
	err = realtime.Serve(w, r, api.options.Repository.Store, api.hub, realtime.StreamOptions{AfterSeq: after, ProjectID: project, Initial: api.state.Snapshots(project)})
	if err != nil && r.Context().Err() == nil {
		api.report(err)
	}
	// The event stream may already have written headers; its transport errors
	// terminate the stream and must never append a JSON error to SSE.
	return nil
}
func (api *API) registerReads() {
	repo := api.options.Repository
	api.add("GET", "/api/projects", func(r *http.Request) (any, int, error) {
		ids := r.URL.Query()["id"]
		if len(ids) > 128 {
			return nil, 0, badInput("id")
		}
		// Start SSE before any changes that could happen while the list is read.
		seq, err := repo.Store.LastSeq(r.Context())
		if err != nil {
			return nil, 0, err
		}
		list := repo.ListProjects
		if len(ids) > 0 {
			list = func(ctx context.Context) ([]core.ProjectView, error) { return repo.ListProjectsByID(ctx, ids) }
		}
		projects, err := list(r.Context())
		if err != nil {
			return nil, 0, err
		}
		return object{"projects": projects, "lastSeq": seq}, 200, nil
	})
	api.add("GET", "/api/projects/:projectId", func(r *http.Request) (any, int, error) {
		id := r.PathValue("projectId")
		detail, err := repo.GetProjectDetail(r.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		delta := api.syncer.Delta(id, detail.Threads, r.URL.Query().Get("since"))
		sync := object{"cursor": delta.Cursor, "partial": delta.Partial}
		if delta.IDs != nil {
			sync["ids"] = delta.IDs
		}
		hasTracker := api.options.HasTracker != nil && api.options.HasTracker(id)
		return object{"project": detail.Project, "threads": delta.Threads, "needsMe": detail.NeedsMe, "sync": sync, "hasTracker": hasTracker}, 200, nil
	})
	api.add("GET", "/api/projects/:projectId/search", func(r *http.Request) (any, int, error) {
		value, err := repo.Search(r.Context(), r.PathValue("projectId"), prefix(r.URL.Query().Get("q"), 200))
		return value, 200, err
	})
	api.add("GET", "/api/projects/:projectId/memory", func(r *http.Request) (any, int, error) {
		value, err := repo.ListMemories(r.Context(), r.PathValue("projectId"))
		return object{"memories": value}, 200, err
	})
	api.add("GET", "/api/projects/:projectId/artifacts", func(r *http.Request) (any, int, error) {
		value, err := repo.ListArtifacts(r.Context(), r.PathValue("projectId"))
		return object{"artifacts": value}, 200, err
	})
	api.add("GET", "/api/threads/:threadId/reviews", func(r *http.Request) (any, int, error) {
		value, err := repo.ListReviews(r.Context(), r.PathValue("threadId"))
		return object{"rounds": value}, 200, err
	})
	api.add("GET", "/api/threads/:threadId/line-comments", func(r *http.Request) (any, int, error) {
		value, err := repo.ListLineComments(r.Context(), r.PathValue("threadId"))
		return object{"comments": value}, 200, err
	})
	api.add("GET", "/api/conversations/:conversationId", api.conversation)
}

var pageLimit = regexp.MustCompile(`^[1-9]\d{0,2}$`)
var pageSince = regexp.MustCompile(`^\d{1,15}$`)

func (api *API) conversation(r *http.Request) (any, int, error) {
	repo := api.options.Repository
	id := r.PathValue("conversationId")
	conversation, err := repo.GetConversation(r.Context(), id)
	if err != nil {
		return nil, 0, err
	}
	query := r.URL.Query()
	cursor := core.MessageCursor{}
	if query.Has("before") {
		value := query.Get("before")
		cursor.Before = &value
	}
	if query.Has("from") {
		value := query.Get("from")
		cursor.From = &value
	}
	if query.Has("limit") {
		raw := query.Get("limit")
		if !pageLimit.MatchString(raw) {
			return nil, 0, badInput("limit")
		}
		value, _ := strconv.Atoi(raw)
		cursor.Limit = &value
	}
	if query.Has("since") {
		raw := query.Get("since")
		if !pageSince.MatchString(raw) {
			return nil, 0, badInput("since")
		}
		value, _ := strconv.ParseInt(raw, 10, 64)
		cursor.Since = &value
	}
	page, err := repo.PageMessages(r.Context(), id, cursor)
	if err != nil {
		return nil, 0, err
	}
	seq, err := repo.Store.LastSeq(r.Context())
	if err != nil {
		return nil, 0, err
	}
	todo, err := repo.GetTodo(r.Context(), id)
	if err != nil {
		return nil, 0, err
	}
	var model any
	if api.options.ModelOf != nil {
		model, err = api.options.ModelOf(r, conversation)
	}
	return object{"conversation": conversation, "messages": page.Messages, "notices": page.Notices, "more": page.More, "lastSeq": seq, "todo": todo, "model": model}, 200, err
}
