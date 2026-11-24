package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
)

const (
	minStaticCompressBytes   = 256
	maxStaticCacheFileBytes  = 8 << 20
	maxStaticCacheTotalBytes = 64 << 20
)

type staticCacheEntry struct {
	name     string
	size     int64
	modTime  time.Time
	raw      []byte
	baseHash string

	brOnce sync.Once
	brData []byte

	gzOnce sync.Once
	gzData []byte
}

type staticAssetCache struct {
	mu         sync.RWMutex
	entries    map[string]*staticCacheEntry
	totalBytes int64
}

func newStaticAssetCache() *staticAssetCache {
	return &staticAssetCache{entries: map[string]*staticCacheEntry{}}
}

func (c *staticAssetCache) get(web fs.FS, name string, info fs.FileInfo) (*staticCacheEntry, error) {
	if c != nil && info.Size() <= maxStaticCacheFileBytes {
		c.mu.RLock()
		entry := c.entries[name]
		c.mu.RUnlock()
		if entry != nil && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
			return entry, nil
		}
	}
	raw, err := fs.ReadFile(web, name)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	entry := &staticCacheEntry{
		name:     name,
		size:     info.Size(),
		modTime:  info.ModTime(),
		raw:      raw,
		baseHash: hex.EncodeToString(sum[:16]),
	}
	if c != nil && int64(len(raw)) <= maxStaticCacheFileBytes {
		c.mu.Lock()
		if existing := c.entries[name]; existing != nil {
			c.totalBytes -= int64(len(existing.raw))
		}
		if c.totalBytes+int64(len(raw)) > maxStaticCacheTotalBytes {
			c.entries = map[string]*staticCacheEntry{}
			c.totalBytes = 0
		}
		c.entries[name] = entry
		c.totalBytes += int64(len(raw))
		c.mu.Unlock()
	}
	return entry, nil
}

func (e *staticCacheEntry) encoded(web fs.FS, encoding string) []byte {
	switch encoding {
	case "br":
		e.brOnce.Do(func() {
			e.brData = readPrecompressed(web, e.name+".br", e.modTime, len(e.raw))
		})
		return e.brData
	case "gzip":
		e.gzOnce.Do(func() {
			if data := readPrecompressed(web, e.name+".gz", e.modTime, len(e.raw)); data != nil {
				e.gzData = data
				return
			}
			if len(e.raw) < minStaticCompressBytes {
				return
			}
			var buf bytes.Buffer
			writer, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
			if err != nil {
				return
			}
			if _, err := writer.Write(e.raw); err != nil {
				return
			}
			if err := writer.Close(); err != nil {
				return
			}
			if buf.Len() < len(e.raw) {
				e.gzData = buf.Bytes()
			}
		})
		return e.gzData
	default:
		return nil
	}
}

func (e *staticCacheEntry) etag(encoding string) string {
	if encoding == "" {
		return `"` + e.baseHash + `"`
	}
	return `"` + e.baseHash + `-` + encoding + `"`
}

func readPrecompressed(web fs.FS, sidecar string, rawModTime time.Time, rawLen int) []byte {
	info, err := fs.Stat(web, sidecar)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return nil
	}
	if !rawModTime.IsZero() && !info.ModTime().IsZero() && info.ModTime().Before(rawModTime) {
		return nil
	}
	data, err := fs.ReadFile(web, sidecar)
	if err != nil || len(data) == 0 || len(data) >= rawLen {
		return nil
	}
	return data
}

func staticContentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	case ".json":
		return "application/json; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".wasm":
		return "application/wasm"
	}
	return mime.TypeByExtension(ext)
}

func isStaticAssetPath(name string) bool {
	if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "icons/") {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs", ".css", ".map", ".webmanifest", ".json", ".svg", ".png", ".ico", ".webp", ".jpg", ".jpeg", ".gif", ".woff", ".woff2", ".ttf", ".otf", ".txt", ".xml", ".wasm", ".br", ".gz":
		return true
	default:
		return false
	}
}

func isCompressibleStaticAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".js", ".mjs", ".css", ".svg", ".json", ".webmanifest", ".map", ".txt", ".xml", ".wasm":
		return true
	default:
		return false
	}
}

func isPrecompressedSidecar(web fs.FS, name string) bool {
	var base string
	switch {
	case strings.HasSuffix(name, ".br"):
		base = strings.TrimSuffix(name, ".br")
	case strings.HasSuffix(name, ".gz"):
		base = strings.TrimSuffix(name, ".gz")
	default:
		return false
	}
	if base == "" {
		return false
	}
	info, err := fs.Stat(web, base)
	return err == nil && !info.IsDir()
}

// acceptedEncodings parses Accept-Encoding per RFC 9110 §12.5.3 and returns
// supported encodings ("br", "gzip") ordered by descending preference.
func acceptedEncodings(header string) []string {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	var (
		brQ, gzipQ, starQ         float64
		brSeen, gzipSeen, starSet bool
	)
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		coding := part
		q := 1.0
		valid := true
		if semi := strings.IndexByte(part, ';'); semi >= 0 {
			coding = strings.TrimSpace(part[:semi])
			params := part[semi+1:]
			for param := range strings.SplitSeq(params, ";") {
				param = strings.TrimSpace(param)
				if param == "" {
					continue
				}
				k, v, ok := strings.Cut(param, "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
					continue
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					valid = false
					break
				}
				q = parsed
			}
		}
		if !valid {
			continue
		}
		switch strings.ToLower(coding) {
		case "br":
			if !brSeen || q > brQ {
				brQ = q
			}
			brSeen = true
		case "gzip":
			if !gzipSeen || q > gzipQ {
				gzipQ = q
			}
			gzipSeen = true
		case "*":
			if !starSet || q > starQ {
				starQ = q
			}
			starSet = true
		}
	}
	if !brSeen && starSet {
		brQ = starQ
	}
	if !gzipSeen && starSet {
		gzipQ = starQ
	}
	switch {
	case brQ > 0 && gzipQ > 0:
		if gzipQ > brQ {
			return []string{"gzip", "br"}
		}
		return []string{"br", "gzip"}
	case brQ > 0:
		return []string{"br"}
	case gzipQ > 0:
		return []string{"gzip"}
	default:
		return nil
	}
}

// etagMatches checks an If-None-Match header against etag using weak comparison
// as required by RFC 9110 §13.1.2.
func etagMatches(ifNoneMatch, etag string) bool {
	if etag == "" {
		return false
	}
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	target := strings.TrimPrefix(etag, "W/")
	rest := ifNoneMatch
	for len(rest) > 0 {
		rest = strings.TrimLeft(rest, " \t,")
		if rest == "" {
			break
		}
		candidate := rest
		if strings.HasPrefix(candidate, "W/") {
			candidate = candidate[2:]
		}
		if len(candidate) == 0 || candidate[0] != '"' {
			if idx := strings.IndexByte(rest, ','); idx >= 0 {
				rest = rest[idx+1:]
				continue
			}
			break
		}
		end := strings.IndexByte(candidate[1:], '"')
		if end < 0 {
			break
		}
		quoted := candidate[:end+2]
		if quoted == target {
			return true
		}
		consumed := len(rest) - len(candidate) + end + 2
		rest = rest[consumed:]
	}
	return false
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
	if isPrecompressedSidecar(api.options.Web, name) {
		domainerr.Write(w, domainerr.NotFound(r.URL.Path))
		return
	}
	info, err := fs.Stat(api.options.Web, name)
	if err != nil || info.IsDir() {
		if isStaticAssetPath(name) {
			domainerr.Write(w, domainerr.NotFound(r.URL.Path))
			return
		}
		name = "index.html"
		info, err = fs.Stat(api.options.Web, name)
	}
	if err != nil || info.IsDir() {
		if err != nil {
			api.report(err)
		}
		domainerr.Write(w, domainerr.NotFound(r.URL.Path))
		return
	}
	entry, err := api.staticAssets.get(api.options.Web, name, info)
	if err != nil {
		api.report(err)
		domainerr.Write(w, domainerr.NotFound(r.URL.Path))
		return
	}

	compressible := isCompressibleStaticAsset(name)
	encoding := ""
	payload := entry.raw
	if compressible {
		w.Header().Set("Vary", "Accept-Encoding")
		acceptHeader := strings.Join(r.Header.Values("Accept-Encoding"), ",")
		for _, candidate := range acceptedEncodings(acceptHeader) {
			if compressed := entry.encoded(api.options.Web, candidate); len(compressed) > 0 {
				encoding = candidate
				payload = compressed
				break
			}
		}
	}

	cacheControl := "no-cache"
	if strings.HasPrefix(name, "assets/") {
		cacheControl = "public, max-age=31536000, immutable"
	}
	etag := entry.etag(encoding)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", etag)

	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && etagMatches(strings.Join(r.Header.Values("If-None-Match"), ","), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if contentType := staticContentType(name); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload)
	}
}
