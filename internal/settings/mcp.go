package settings

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/mcp"
	"github.com/daidr/Ariadne/internal/textutil"
)

var mcpID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,19}$`)

func realPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	full, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	// Resolve the existing parent as well: a not-yet-created directory below a
	// junction must not bypass the private application directory check.
	missing := []string{}
	candidate := full
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return full
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}

func (store *Store) MCPServers() ([]mcp.ServerConfig, error) {
	data, err := json.Marshal(object(store.snapshot()["mcp"])["servers"])
	if err != nil {
		return nil, err
	}
	var servers []mcp.ServerConfig
	if json.Unmarshal(data, &servers) != nil {
		return nil, bad("servers")
	}
	if servers == nil {
		servers = []mcp.ServerConfig{}
	}
	return servers, nil
}
func insideDir(path, directory string) bool {
	relative, err := filepath.Rel(realPath(directory), realPath(path))
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
func (store *Store) PublicMCP() []any {
	servers := []any{}
	for _, raw := range list(object(store.snapshot()["mcp"])["servers"]) {
		server := cloneMap(object(raw))
		server["env"] = names(object(server["env"]))
		server["headers"] = names(object(server["headers"]))
		owned, _ := clone(server)
		servers = append(servers, owned)
	}
	return servers
}
func (store *Store) UpdateMCP(input any) error {
	body := object(input)
	items, ok := body["servers"].([]any)
	if !ok {
		return bad("servers")
	}
	return store.update(func(config Object) error {
		previous := list(object(config["mcp"])["servers"])
		ids := map[string]bool{}
		servers := []any{}
		for index, raw := range items {
			entry := object(raw)
			at := "servers[" + strconv.Itoa(index) + "]"
			id := text(entry["id"])
			if !mcpID.MatchString(id) || ids[id] {
				return bad(at + ".id")
			}
			ids[id] = true
			before := Object{}
			for _, raw := range previous {
				candidate := object(raw)
				if candidate["id"] == id {
					before = candidate
					break
				}
			}
			name := textutil.Trim(text(entry["name"]))
			if name == "" {
				name = id
			}
			tools := Object{}
			for key, value := range object(entry["tools"]) {
				mark := text(value)
				if !hasString([]string{"read", "ask", "off"}, mark) {
					return bad(at + ".tools")
				}
				if mark != "ask" {
					tools[key] = mark
				}
			}
			server := Object{"id": id, "name": name, "tools": tools}
			switch entry["transport"] {
			case "stdio":
				command := textutil.Trim(text(entry["command"]))
				if command == "" {
					return bad(at + ".command")
				}
				args, ok := fallback(entry["args"], []any{}).([]any)
				if !ok {
					return bad(at + ".args")
				}
				for _, arg := range args {
					if _, ok := arg.(string); !ok {
						return bad(at + ".args")
					}
				}
				cwd := textutil.Trim(text(entry["cwd"]))
				if cwd != "" && insideDir(cwd, filepath.Dir(store.path)) {
					return bad(at + ".cwd")
				}
				env, err := secretPairs(entry["env"], envName.MatchString, object(before["env"]), at+".env")
				if err != nil {
					return err
				}
				server["transport"] = "stdio"
				server["command"] = command
				server["args"] = args
				if cwd != "" {
					server["cwd"] = cwd
				}
				if len(env) > 0 {
					server["env"] = env
				}
			case "http":
				address := textutil.Trim(text(entry["url"]))
				parsed, err := url.Parse(address)
				if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil {
					return bad(at + ".url")
				}
				headers, err := secretPairs(entry["headers"], headerName.MatchString, object(before["headers"]), at+".headers")
				if err != nil {
					return err
				}
				server["transport"] = "http"
				server["url"] = address
				if len(headers) > 0 {
					server["headers"] = headers
				}
			default:
				return bad(at + ".transport")
			}
			servers = append(servers, server)
		}
		config["mcp"] = Object{"servers": servers}
		return nil
	})
}
