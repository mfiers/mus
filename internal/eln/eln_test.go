package eln

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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
