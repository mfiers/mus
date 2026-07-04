package eln

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListSampleTypes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sampleTypes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"sampleTypeID": 12, "name": "Data"},
				{"sampleTypeID": 34, "name": "DNA"},
			},
			"totalRecords": 2,
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	types, err := c.ListSampleTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 sample types; got %d", len(types))
	}
	if types[0].ID != 12 || types[0].Name != "Data" {
		t.Errorf("first type: %+v", types[0])
	}
}

func TestFindSampleTypeByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sampleTypes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"sampleTypeID": 12, "name": "Data"},
				{"sampleTypeID": 34, "name": "DNA"},
			},
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")

	// Exact + case-insensitive match.
	got, err := c.FindSampleTypeByName("data")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 12 {
		t.Errorf("case-insensitive match: got id=%d, want 12", got.ID)
	}

	// Not found — error should list what IS available.
	_, err = c.FindSampleTypeByName("Nonexistent")
	if err == nil {
		t.Fatal("expected error for missing sample type")
	}
	if !strings.Contains(err.Error(), "Data") || !strings.Contains(err.Error(), "DNA") {
		t.Errorf("error should list available types; got %q", err.Error())
	}
}

func TestCreateSample(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/samples", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["sampleTypeID"].(float64) != 12 {
			t.Errorf("sampleTypeID: got %v, want 12", req["sampleTypeID"])
		}
		if req["name"] != "foo.tsv" {
			t.Errorf("name: got %v", req["name"])
		}
		if req["description"] != "the message" {
			t.Errorf("description: got %v", req["description"])
		}
		// Return the full object shape.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sampleID":    777,
			"name":         "foo.tsv",
			"description":  "the message",
			"sampleTypeID": 12,
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	got, err := c.CreateSample(CreateSampleReq{
		SampleTypeID: 12,
		Name:         "foo.tsv",
		Description:  "the message",
		Note:         "path=/tmp/foo.tsv\nsha256=abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 777 {
		t.Errorf("got id=%d, want 777", got.ID)
	}
}

func TestCreateSample_RejectsEmptyName(t *testing.T) {
	c := New("http://ignored", "k")
	if _, err := c.CreateSample(CreateSampleReq{SampleTypeID: 12}); err == nil {
		t.Error("expected error for empty Name")
	}
	if _, err := c.CreateSample(CreateSampleReq{Name: "x"}); err == nil {
		t.Error("expected error for zero SampleTypeID")
	}
}

func TestCreateExperimentSection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/1234/sections", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		_ = json.Unmarshal(body, &got)
		if got["sectionType"] != "SAMPLESOUT" {
			t.Errorf("sectionType: got %q, want SAMPLESOUT", got["sectionType"])
		}
		if got["sectionHeader"] != "batch header" {
			t.Errorf("header: got %q", got["sectionHeader"])
		}
		_, _ = w.Write([]byte("999"))
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	journalID, err := c.CreateExperimentSection(1234, "SAMPLESOUT", "batch header")
	if err != nil {
		t.Fatal(err)
	}
	if journalID != 999 {
		t.Errorf("journalID: got %d, want 999", journalID)
	}
}

func TestLinkSamplesToSection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/experiments/sections/999/samples", func(w http.ResponseWriter, r *http.Request) {
		// eLabNext requires PUT — POST returns 405.
		if r.Method != http.MethodPut {
			t.Errorf("method %s, want PUT", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var got []int64
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body should be a JSON array of int; got %s", body)
		}
		if len(got) != 3 || got[0] != 111 || got[1] != 222 || got[2] != 333 {
			t.Errorf("linked ids: %v", got)
		}
		_, _ = w.Write([]byte("null"))
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	if err := c.LinkSamplesToSection(999, []int64{111, 222, 333}); err != nil {
		t.Fatal(err)
	}
}

func TestLinkSamplesToSection_EmptyIsNoOp(t *testing.T) {
	// No server needed — the empty-slice guard should skip the request.
	c := New("http://never-called", "k")
	if err := c.LinkSamplesToSection(999, nil); err != nil {
		t.Errorf("empty slice should be a silent no-op; got %v", err)
	}
}

func TestGetSample_Active(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/samples/12345", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sampleID":     12345,
			"name":         "foo.tsv",
			"description":  "some data",
			"sampleTypeID": 12,
			"archived":     false,
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	s, err := c.GetSample(12345)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 12345 || s.Archived {
		t.Errorf("got %+v; want ID=12345 archived=false", s)
	}
}

func TestGetSample_Archived(t *testing.T) {
	// Archived samples remain queryable via GET /samples/{id}. This
	// is the whole point of the lookaside — mus needs to know when
	// the depositor archived a sample so it can recreate it instead
	// of blindly reusing a dead SampleID.
	mux := http.NewServeMux()
	mux.HandleFunc("/samples/99", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sampleID":     99,
			"name":         "foo.tsv",
			"sampleTypeID": 12,
			"archived":     true,
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	s, err := c.GetSample(99)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Archived {
		t.Errorf("archived flag lost: %+v", s)
	}
}

func TestGetSample_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/samples/404", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	_, err := c.GetSample(404)
	if err == nil {
		t.Fatal("expected error for missing sample")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound should recognise 404; got %v (%T)", err, err)
	}
}

func TestGetSample_EmptyBodyIsNotFound(t *testing.T) {
	// Some tenants have been observed returning 200 with an empty
	// body for missing IDs. Treat that as not-found so callers
	// re-create rather than skip.
	mux := http.NewServeMux()
	mux.HandleFunc("/samples/12345", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// deliberately empty body
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	_, err := c.GetSample(12345)
	if err == nil {
		t.Fatal("expected not-found for empty body")
	}
	if !IsNotFound(err) {
		t.Errorf("empty body should map to IsNotFound; got %v", err)
	}
}

func TestListSampleTypeMetas(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sampleTypes/42/meta", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"sampleTypeMetaID": 100, "key": "path", "sampleDataType": "TEXT"},
				{"sampleTypeMetaID": 101, "key": "sha256sum", "sampleDataType": "TEXT"},
				{"sampleTypeMetaID": 102, "key": "size_bytes", "sampleDataType": "NUMERIC"},
			},
		})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	metas, err := c.ListSampleTypeMetas(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("got %d metas, want 3", len(metas))
	}
	if metas[0].SampleTypeMetaID != 100 || metas[0].Key != "path" || metas[0].SampleDataType != "TEXT" {
		t.Errorf("first meta: %+v", metas[0])
	}
	if metas[2].SampleDataType != "NUMERIC" {
		t.Errorf("third meta datatype: got %q, want NUMERIC", metas[2].SampleDataType)
	}
}

func TestCreateSample_WithMetas(t *testing.T) {
	// The sampleMetas array must round-trip verbatim to POST /samples.
	// eLabNext validates the keys against the sample type's declared
	// fields, so mus must send the exact key the tenant configured
	// (case preserved by metasForType).
	mux := http.NewServeMux()
	mux.HandleFunc("/samples", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			SampleTypeID int64 `json:"sampleTypeID"`
			Name         string
			SampleMetas  []map[string]string `json:"sampleMetas"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.SampleMetas) != 2 {
			t.Fatalf("sampleMetas count: got %d, want 2 — body was %s",
				len(req.SampleMetas), body)
		}
		byKey := map[string]map[string]string{}
		for _, m := range req.SampleMetas {
			byKey[m["key"]] = m
		}
		if byKey["path"]["value"] != "/local/foo.tsv" {
			t.Errorf("path meta: %+v", byKey["path"])
		}
		if byKey["path"]["sampleDataType"] != "TEXT" {
			t.Errorf("path dataType: %q", byKey["path"]["sampleDataType"])
		}
		if byKey["sha256sum"]["value"] != "abc123" {
			t.Errorf("sha256sum meta: %+v", byKey["sha256sum"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sampleID": 55})
	})
	srv := httptest.NewServer(authMiddleware(t, "k", mux))
	defer srv.Close()

	c := New(srv.URL, "k")
	s, err := c.CreateSample(CreateSampleReq{
		SampleTypeID: 12,
		Name:         "foo.tsv",
		SampleMetas: []SampleMetaNew{
			{Key: "path", SampleDataType: "TEXT", Value: "/local/foo.tsv"},
			{Key: "sha256sum", SampleDataType: "TEXT", Value: "abc123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 55 {
		t.Errorf("sampleID: %d", s.ID)
	}
}

func TestIsNotFound_NonHTTPError(t *testing.T) {
	// A plain error is NOT a 404 — mus must not misclassify network
	// hiccups as "sample gone, please recreate".
	if IsNotFound(errNonHTTP{}) {
		t.Error("plain error should not be treated as 404")
	}
}

type errNonHTTP struct{}

func (errNonHTTP) Error() string { return "network broken" }
