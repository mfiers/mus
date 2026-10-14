package eln

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExpInfo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1234", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "Exp One", "studyID": 99})
	})
	mux.HandleFunc("/experiments/1234/collaborators", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"firstName": "Alice", "lastName": "Doe"}},
		})
	})
	mux.HandleFunc("/studies", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("studyID") != "99" {
			t.Errorf("unexpected studyID query: %q", r.URL.Query().Get("studyID"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"recordCount": 1,
			"data":        []map[string]any{{"name": "Study A", "projectID": 7}},
		})
	})
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"recordCount": 1,
			"data":        []map[string]any{{"name": "Project Z"}},
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	info, err := c.ExpInfo(1234)
	if err != nil {
		t.Fatal(err)
	}
	want := &ExperimentInfo{
		ExperimentID: 1234, ExperimentName: "Exp One",
		StudyID: 99, StudyName: "Study A",
		ProjectID: 7, ProjectName: "Project Z",
		Collaborators: []string{"Alice Doe"},
	}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("info = %+v, want %+v", info, want)
	}
}

func TestFixExperimentID(t *testing.T) {
	cases := map[int64]int64{
		1234:                  1234,
		1_000_000_000_292_564: 292564,
	}
	for in, want := range cases {
		if got := FixExperimentID(in); got != want {
			t.Errorf("Fix(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCreateCommentSection(t *testing.T) {
	var capturedBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1234/sections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(int64(555))
	})
	mux.HandleFunc("/experiments/sections/555/content", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("method = %s", r.Method)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedBody = body["contents"]
		_ = json.NewEncoder(w).Encode(struct{}{})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()
	c := New(srv.URL, "k")
	jid, err := c.CreateCommentSection(1234, "hello", "<p>world</p>")
	if err != nil {
		t.Fatal(err)
	}
	if jid != 555 {
		t.Errorf("journal id = %d", jid)
	}
	if capturedBody != "<p>world</p>" {
		t.Errorf("captured body = %q", capturedBody)
	}
}

// TestCreateCommentSectionEmptyPUTResponse pins the fix for the "set section
// content: EOF" bug — eLabJournal returns 2xx with an empty body for the PUT
// .../sections/<id>/content endpoint, which used to make the JSON decoder
// fail. Empty 2xx must be treated as success.
func TestCreateCommentSectionEmptyPUTResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1234/sections", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(int64(555))
	})
	mux.HandleFunc("/experiments/sections/555/content", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // body intentionally left empty — mirrors real ELN
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()
	c := New(srv.URL, "k")
	jid, err := c.CreateCommentSection(1234, "hello", "<p>world</p>")
	if err != nil {
		t.Fatalf("empty 2xx body should be treated as success, got: %v", err)
	}
	if jid != 555 {
		t.Errorf("journal id = %d, want 555", jid)
	}
}

func TestCurrentUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/getCurrentUserInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"userID":       42,
			"firstName":    "Ada",
			"lastName":     "Lovelace",
			"emailAddress": "ada@example.com",
		})
	})
	mux.HandleFunc("/badauth/users/getCurrentUserInfo", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(authMiddleware(t, "good-key", mux))
	defer srv.Close()

	c := New(srv.URL, "good-key")
	u, err := c.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	if u.UserID != 42 || u.FullName() != "Ada Lovelace" {
		t.Errorf("CurrentUser = %+v", u)
	}
}

func TestCurrentUserBadAuthFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/getCurrentUserInfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "good-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"userID": 1, "firstName": "x"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "wrong-key")
	if _, err := c.CurrentUser(); err == nil {
		t.Errorf("expected error for wrong key")
	} else if !strings.Contains(err.Error(), "401") {
		t.Errorf("error did not mention 401: %v", err)
	}
}

func TestFullNameFallsBackToEmail(t *testing.T) {
	u := &UserInfo{Email: "x@y"}
	if u.FullName() != "x@y" {
		t.Errorf("FullName = %q", u.FullName())
	}
}

func TestUploadFile_RawBody_NotMultipart(t *testing.T) {
	// Regression: the eLabNext endpoint expects RAW binary body,
	// NOT multipart/form-data. Wrapping in multipart used to cause
	// HTTP 500 with an empty response body (server refuses the
	// wrapper without a diagnostic). This test pins the wire
	// format so future refactors can't silently reintroduce it.
	tmp := t.TempDir()
	fpath := filepath.Join(tmp, "hello.txt")
	payload := []byte("hello world\n")
	if err := os.WriteFile(fpath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	var seenCT string
	var seenBody []byte
	var seenFileName string
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/sections/16152172/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s, want POST", r.Method)
		}
		seenCT = r.Header.Get("Content-Type")
		seenFileName = r.URL.Query().Get("fileName")
		var err error
		seenBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	if err := c.UploadFile(16152172, fpath, "hello.txt"); err != nil {
		t.Fatal(err)
	}

	// Body must be verbatim file contents — NOT wrapped in a
	// multipart envelope (multipart bodies begin with `--<boundary>`
	// and include Content-Disposition headers).
	if string(seenBody) != string(payload) {
		t.Errorf("body: got %q, want %q", seenBody, payload)
	}
	if strings.HasPrefix(string(seenBody), "--") {
		t.Errorf("body appears to be multipart-wrapped: %q", seenBody[:min(64, len(seenBody))])
	}
	if strings.Contains(seenCT, "multipart") {
		t.Errorf("Content-Type must not be multipart; got %q", seenCT)
	}
	if seenCT != "application/octet-stream" {
		t.Errorf("Content-Type: got %q, want application/octet-stream", seenCT)
	}
	if seenFileName != "hello.txt" {
		t.Errorf("fileName query param: got %q, want hello.txt", seenFileName)
	}
}

func authMiddleware(t *testing.T, key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != key {
			t.Errorf("auth header = %q, want %q", r.Header.Get("Authorization"), key)
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/json") {
			t.Errorf("accept header = %q", r.Header.Get("Accept"))
		}
		next.ServeHTTP(w, r)
	})
}

func TestFixStudyID(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"plain small id unchanged", 411192, 411192},
		{"long UI form of Very Old Projects", 12000000411192, 411192},
		{"long form with leading zero stripped by int parse", 12000000411192, 411192},
		{"another long id", 12000000218872, 218872},
		{"small id at boundary stays", 99_999_999, 99_999_999},
		{"zero stays zero", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FixStudyID(tc.in); got != tc.want {
				t.Errorf("FixStudyID(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// do() must retry transient 5xx and succeed when the server recovers.
// Regression: a one-off HTTP 500 on POST .../sections silently dropped an
// upload's ELN sample record.
func TestDo_RetriesTransient5xx(t *testing.T) {
	// Shrink backoff so the test is fast.
	origBackoff := elnRetryBackoff
	elnRetryBackoff = 1 * time.Millisecond
	t.Cleanup(func() { elnRetryBackoff = origBackoff })

	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1/sections", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 { // fail twice, then succeed
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("16194202"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL, "k")

	id, err := c.CreateExperimentSection(1, "SAMPLESOUT", "hdr")
	if err != nil {
		t.Fatalf("expected success after retries, got %v (calls=%d)", err, calls)
	}
	if id != 16194202 {
		t.Errorf("section id = %d, want 16194202", id)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts (2 fail + 1 ok), got %d", calls)
	}
}

// do() must NOT retry 4xx — they're deterministic. One call, immediate error.
func TestDo_NoRetryOn4xx(t *testing.T) {
	origBackoff := elnRetryBackoff
	elnRetryBackoff = 1 * time.Millisecond
	t.Cleanup(func() { elnRetryBackoff = origBackoff })

	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1/sections", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"no permission"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL, "k")

	_, err := c.CreateExperimentSection(1, "SAMPLESOUT", "hdr")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if calls != 1 {
		t.Errorf("4xx must not retry: expected 1 call, got %d", calls)
	}
}

// do() exhausts retries and returns the last 5xx error.
func TestDo_GivesUpAfterMaxAttempts(t *testing.T) {
	origBackoff := elnRetryBackoff
	elnRetryBackoff = 1 * time.Millisecond
	t.Cleanup(func() { elnRetryBackoff = origBackoff })

	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1/sections", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL, "k")

	_, err := c.CreateExperimentSection(1, "SAMPLESOUT", "hdr")
	if err == nil {
		t.Fatal("expected failure after exhausting retries")
	}
	if calls != elnMaxAttempts {
		t.Errorf("expected %d attempts, got %d", elnMaxAttempts, calls)
	}
}

// sanitizeSectionHeader replaces the ASCII apostrophe (which makes the
// eLabNext section-create endpoint return HTTP 500) with U+2019, and
// leaves other characters untouched.
func TestSanitizeSectionHeader(t *testing.T) {
	cases := []struct{ in, want string }{
		{"no quotes here", "no quotes here"},
		{"Sriram's work", "Sriram’s work"},
		{"it's a 'test'", "it’s a ’test’"},
		{`double "q" and a&b <c> 50% a\b ;`, `double "q" and a&b <c> 50% a\b ;`},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeSectionHeader(c.in); got != c.want {
			t.Errorf("sanitizeSectionHeader(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
