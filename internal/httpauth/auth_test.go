package httpauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthenticationCannotBeBypassedWithQueryOrFileCookie(t *testing.T) {
	auth := Auth{Token: "secret", Now: func() time.Time { return time.UnixMilli(1800000000000) }}
	file := httptest.NewRequest("GET", "http://localhost:7428/api/files/fil_a", nil)
	cookie := auth.FileSessionCookie(file)
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, test := range []struct {
		method, path, bearer, cookie, site string
		want                               int
	}{
		{"GET", "/api/health", "", "", "", 204},
		{"POST", "/api/health", "", "", "", 401},
		{"GET", "/api/projects", "", "", "", 401},
		{"GET", "/api/projects?token=secret", "", "", "", 401},
		{"GET", "/api/files/fil_missing", "", "", "", 401},
		{"HEAD", "/api/files/fil_missing", "", "", "", 401},
		{"GET", "/api/events?token=secret", "", "", "", 401},
		{"GET", "/api/artifacts/art_a/live?token=secret", "", "", "", 204},
		{"GET", "/api/projects", "Bearer secret", "", "", 204},
		{"GET", "/api/files/fil_a", "", cookie.String(), "same-origin", 204},
		{"HEAD", "/api/files/fil_a", "", cookie.String(), "none", 204},
		{"GET", "/api/projects", "", cookie.String(), "", 401},
		{"POST", "/api/files/fil_a", "", cookie.String(), "", 401},
		{"GET", "/api/files/fil_a", "Bearer wrong", cookie.String(), "", 401},
		{"GET", "/api/files/fil_a", "", cookie.String(), "cross-site", 401},
		{"GET", "/api/files/fil_a", "", cookie.String(), "same-site", 401},
		{"GET", "/api/files/fil_a", "", cookie.String() + "; " + cookie.Name + "=" + cookie.Value, "", 401},
	} {
		t.Run(fmt.Sprintf("%s %s %s %s", test.method, test.path, test.bearer, test.site), func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://localhost:7428"+test.path, nil)
			if test.bearer != "" {
				request.Header.Set("Authorization", test.bearer)
			}
			request.Header.Set("Cookie", test.cookie)
			request.Header.Set("Sec-Fetch-Site", test.site)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != test.want {
				t.Fatalf("got %d, want %d: %s", result.Code, test.want, result.Body.String())
			}
		})
	}
}

func TestFileSessionExpiryOriginAndTokenRotation(t *testing.T) {
	now := time.UnixMilli(1800000000000)
	auth := Auth{Token: "secret", Now: func() time.Time { return now }}
	request := httptest.NewRequest("GET", "https://localhost:7428/api/files/fil_a", nil)
	cookie := auth.FileSessionCookie(request)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/files/" {
		t.Fatalf("unsafe cookie: %s", cookie.String())
	}
	request.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
	if !auth.ValidFileSession(request) {
		t.Fatal("new session was refused")
	}
	rotated := auth
	rotated.Token = "new-secret"
	if rotated.ValidFileSession(request) {
		t.Fatal("token rotation retained session")
	}
	request.Host = "localhost:8000"
	if auth.ValidFileSession(request) {
		t.Fatal("session crossed origins")
	}
	request.Host = "localhost:7428"
	now = now.Add(FileSessionDuration)
	if auth.ValidFileSession(request) {
		t.Fatal("expired session accepted")
	}
}

func TestBearerHeaderWinsAndEmptyHeaderCannotUseFileSession(t *testing.T) {
	auth := Auth{Token: "secret"}
	request := httptest.NewRequest("GET", "http://localhost/api/files/fil_a", nil)
	cookie := auth.FileSessionCookie(request)
	request.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
	request.Header.Set("Authorization", "")
	if auth.Authorized(request) {
		t.Fatal("empty authorization header used cookie fallback")
	}
	request = httptest.NewRequest("GET", "http://localhost/api/artifacts/art_a/live?token=secret", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	if auth.Bearer(request) {
		t.Fatal("query overrode explicit bearer")
	}
	if strings.Contains(cookie.Value, "secret") {
		t.Fatal("cookie exposed token")
	}
}
