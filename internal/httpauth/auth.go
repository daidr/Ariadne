// Package httpauth preserves bearer and file-only cookie authentication.
package httpauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const FileSessionDuration = 24 * time.Hour

var filePath = regexp.MustCompile(`^/api/files/(fil_[\w-]+)$`)
var livePath = regexp.MustCompile(`^/api/artifacts/(art_[\w-]+)/live$`)
var cookieValue = regexp.MustCompile(`^([0-9]{1,16})\.([A-Za-z0-9_-]{43})$`)

type Auth struct {
	Token string
	Now   func() time.Time
}

func (auth Auth) now() time.Time {
	if auth.Now != nil {
		return auth.Now()
	}
	return time.Now()
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (auth Auth) Bearer(request *http.Request) bool {
	if len(request.Header.Values("Authorization")) > 1 {
		return false
	}
	header := request.Header.Get("Authorization")
	given := ""
	if strings.HasPrefix(header, "Bearer ") {
		given = strings.TrimPrefix(header, "Bearer ")
	} else if request.URL.Path == "/api/events" || livePath.MatchString(request.URL.Path) {
		given = request.URL.Query().Get("token")
	}
	return given != "" && auth.Token != "" && equal(given, auth.Token)
}

func (auth Auth) Authorized(request *http.Request) bool {
	if auth.Bearer(request) {
		return true
	}
	if _, exists := request.Header["Authorization"]; exists {
		return false
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return false
	}
	return filePath.MatchString(request.URL.Path) && auth.ValidFileSession(request)
}

// Middleware authenticates before routing or looking up file existence. Only the
// health check is public under /api/; a file cookie never grants API access.
func (auth Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") &&
			!(request.URL.Path == "/api/health" && request.Method == http.MethodGet) &&
			!auth.Authorized(request) {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.Header().Set("Cache-Control", "no-store")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"key":"error.unauthorized"}}`))
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func Origin(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil || request.URL.Scheme == "https" {
		scheme = "https"
	}
	host := strings.ToLower(request.Host)
	if host == "" {
		host = strings.ToLower(request.URL.Host)
	}
	if scheme == "http" {
		host = strings.TrimSuffix(host, ":80")
	}
	if scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	return scheme + "://" + host
}

func cookieName(origin string) string {
	hash := sha256.Sum256([]byte(origin))
	return "ariadne_files_" + hex.EncodeToString(hash[:])[:12]
}

func signature(token, origin string, until int64) string {
	hash := hmac.New(sha256.New, []byte(token))
	_, _ = fmt.Fprintf(hash, "ariadne:file-read:%s:%d", origin, until)
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func (auth Auth) FileSessionCookie(request *http.Request) *http.Cookie {
	now := auth.now()
	until := now.Truncate(time.Hour).Add(FileSessionDuration).UnixMilli()
	origin := Origin(request)
	return &http.Cookie{
		Name: cookieName(origin), Value: fmt.Sprintf("%d.%s", until, signature(auth.Token, origin, until)),
		Path: "/api/files/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int((until - now.UnixMilli()) / 1000), Secure: strings.HasPrefix(origin, "https://"),
	}
}

func (auth Auth) ValidFileSession(request *http.Request) bool {
	if auth.Token == "" {
		return false
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := Origin(request)
	name := cookieName(origin)
	var values []string
	for _, part := range strings.Split(request.Header.Get("Cookie"), ";") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(part), name+"="); ok {
			values = append(values, value)
		}
	}
	if len(values) != 1 {
		return false
	}
	match := cookieValue.FindStringSubmatch(values[0])
	if match == nil {
		return false
	}
	until, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return false
	}
	now := auth.now().UnixMilli()
	return until > now && until <= now+FileSessionDuration.Milliseconds() && equal(match[2], signature(auth.Token, origin, until))
}
