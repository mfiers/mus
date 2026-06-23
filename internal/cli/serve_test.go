package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dummyHandler is a stand-in for the real mux when we just want
// to check whether the middleware lets a request through.
var dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "ok")
})

func TestWithTokenAuth_RejectsMissingToken(t *testing.T) {
	h := withTokenAuth("the-token", dummyHandler)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no-token request: status %d, want 401", w.Code)
	}
}

func TestWithTokenAuth_RejectsWrongToken(t *testing.T) {
	h := withTokenAuth("the-token", dummyHandler)
	r := httptest.NewRequest(http.MethodGet, "/?t=wrong", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong-token: status %d, want 401", w.Code)
	}
}

func TestWithTokenAuth_AcceptsURLToken_SetsCookie(t *testing.T) {
	h := withTokenAuth("the-token", dummyHandler)
	r := httptest.NewRequest(http.MethodGet, "/?t=the-token", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("url-token: status %d, want 200", w.Code)
	}
	cookies := w.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "mus_token" && c.Value == "the-token" {
			found = true
			if !c.HttpOnly {
				t.Errorf("cookie should be HttpOnly")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie should be SameSite=Strict, got %v", c.SameSite)
			}
		}
	}
	if !found {
		t.Errorf("expected mus_token cookie set on success; got cookies=%v", cookies)
	}
}

func TestWithTokenAuth_AcceptsCookie(t *testing.T) {
	h := withTokenAuth("the-token", dummyHandler)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "mus_token", Value: "the-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("cookie-token: status %d, want 200", w.Code)
	}
}

func TestWithLocalhostGuard_AcceptsLoopback(t *testing.T) {
	h := withLocalhostGuard(dummyHandler)
	for _, addr := range []string{"127.0.0.1:54321", "[::1]:54321"} {
		t.Run(addr, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = addr
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("%s: status %d, want 200", addr, w.Code)
			}
		})
	}
}

func TestWithLocalhostGuard_RejectsNonLoopback(t *testing.T) {
	h := withLocalhostGuard(dummyHandler)
	for _, addr := range []string{"10.0.0.1:54321", "192.168.1.5:54321", "[2001:db8::1]:54321"} {
		t.Run(addr, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = addr
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s: status %d, want 403", addr, w.Code)
			}
		})
	}
}

func TestIsLoopbackBindAddr(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.5.5.5", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"10.0.0.1", false},
		{"192.168.1.5", false},
		{"", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		got := isLoopbackBindAddr(tc.in)
		if got != tc.want {
			t.Errorf("isLoopbackBindAddr(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestGenerateToken(t *testing.T) {
	a, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 { // 32 bytes hex
		t.Errorf("token length: got %d, want 64", len(a))
	}
	b, _ := generateToken()
	if a == b {
		t.Error("two tokens shouldn't collide")
	}
}

func TestConstantTimeEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"", "", true},
		{"abc", "abcd", false},
		{"x", "", false},
	}
	for _, tc := range cases {
		if got := constantTimeEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("constantTimeEqual(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRunServe_RefusesNonLoopbackBind(t *testing.T) {
	var stdout, stderr strings.Builder
	err := runServe(nil, serveOptions{Port: 0, Bind: "0.0.0.0", NoBrowser: true},
		&stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --bind 0.0.0.0")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("expected loopback hint; got %v", err)
	}
}
