// Package eln is a minimal eLabJournal REST client.
//
// Authentication: the API key is sent as the `Authorization` header value
// (yes, raw, no `Bearer` prefix — that's how eLabJournal does it). Base URL
// and key are loaded from the secret store (`eln_url`, `eln_apikey`).
package eln

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client talks to a single eLabJournal instance.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New creates a Client.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// UserInfo is the slice of `users/getCurrentUserInfo` we care about. The
// real response carries many more fields; we ignore them.
type UserInfo struct {
	UserID    int64  `json:"userID"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"emailAddress"`
}

// FullName returns "First Last" or whatever portion of the name was supplied.
func (u *UserInfo) FullName() string {
	if u == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(u.FirstName), strings.TrimSpace(u.LastName)}
	out := strings.TrimSpace(strings.Join(parts, " "))
	if out == "" {
		return u.Email
	}
	return out
}

// CurrentUser hits GET /users/getCurrentUserInfo. Used by `mus eln login` to
// verify an API key before storing it. Returns an error on non-2xx (including
// 401 for a bad token).
func (c *Client) CurrentUser() (*UserInfo, error) {
	var u UserInfo
	if err := c.get("users/getCurrentUserInfo", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ExperimentInfo bundles the project / study / experiment names and IDs we
// need to tag a folder. Collaborators is a list of full names.
type ExperimentInfo struct {
	ExperimentID   int64
	ExperimentName string
	StudyID        int64
	StudyName      string
	ProjectID      int64
	ProjectName    string
	Collaborators  []string
}

// FixExperimentID strips the leading "1" from long-form 16-digit IDs that the
// API rejects.
func FixExperimentID(id int64) int64 {
	if id > 1_000_000_000_000_000 {
		s := strconv.FormatInt(id, 10)
		v, err := strconv.ParseInt(s[1:], 10, 64)
		if err != nil {
			return id
		}
		return v
	}
	return id
}

// ExpInfo fetches experiment + study + project + collaborators for `expID`.
func (c *Client) ExpInfo(expID int64) (*ExperimentInfo, error) {
	expID = FixExperimentID(expID)
	var exp struct {
		Name    string `json:"name"`
		StudyID int64  `json:"studyID"`
	}
	if err := c.get(fmt.Sprintf("experiments/%d", expID), nil, &exp); err != nil {
		return nil, fmt.Errorf("get experiment: %w", err)
	}
	var collabResp struct {
		Data []struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"data"`
	}
	if err := c.get(fmt.Sprintf("experiments/%d/collaborators", expID), nil, &collabResp); err != nil {
		return nil, fmt.Errorf("get collaborators: %w", err)
	}
	var studs struct {
		RecordCount int `json:"recordCount"`
		Data        []struct {
			Name      string `json:"name"`
			ProjectID int64  `json:"projectID"`
		} `json:"data"`
	}
	if err := c.get("studies", url.Values{"studyID": {strconv.FormatInt(exp.StudyID, 10)}}, &studs); err != nil {
		return nil, fmt.Errorf("get study: %w", err)
	}
	if studs.RecordCount != 1 {
		return nil, fmt.Errorf("expected exactly 1 study, got %d", studs.RecordCount)
	}
	study := studs.Data[0]
	var projs struct {
		RecordCount int `json:"recordCount"`
		Data        []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.get("projects", url.Values{"projectID": {strconv.FormatInt(study.ProjectID, 10)}}, &projs); err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if projs.RecordCount != 1 {
		return nil, fmt.Errorf("expected exactly 1 project, got %d", projs.RecordCount)
	}
	info := &ExperimentInfo{
		ExperimentID:   expID,
		ExperimentName: exp.Name,
		StudyID:        exp.StudyID,
		StudyName:      study.Name,
		ProjectID:      study.ProjectID,
		ProjectName:    projs.Data[0].Name,
	}
	for _, c := range collabResp.Data {
		info.Collaborators = append(info.Collaborators, strings.TrimSpace(c.FirstName+" "+c.LastName))
	}
	return info, nil
}

// CreateCommentSection appends a COMMENT section to an experiment and uploads
// the provided HTML body into it.
func (c *Client) CreateCommentSection(expID int64, title, htmlBody string) (int64, error) {
	expID = FixExperimentID(expID)
	body := map[string]string{"sectionType": "COMMENT", "sectionHeader": title}
	var journalID int64
	if err := c.post(fmt.Sprintf("experiments/%d/sections", expID), body, &journalID); err != nil {
		return 0, fmt.Errorf("create comment section: %w", err)
	}
	contentBody := map[string]string{"contents": htmlBody}
	var ignored any
	if err := c.put(fmt.Sprintf("experiments/sections/%d/content", journalID), contentBody, &ignored); err != nil {
		return journalID, fmt.Errorf("set section content: %w", err)
	}
	return journalID, nil
}

// CreateFileSection appends a FILE section and returns the new journal ID.
func (c *Client) CreateFileSection(expID int64, title string) (int64, error) {
	expID = FixExperimentID(expID)
	body := map[string]string{"sectionType": "FILE", "sectionHeader": title}
	var journalID int64
	if err := c.post(fmt.Sprintf("experiments/%d/sections", expID), body, &journalID); err != nil {
		return 0, err
	}
	return journalID, nil
}

// --- samples ---------------------------------------------------------------

// SampleType is one row from GET /sampleTypes. Every sample created via
// POST /samples must reference a sampleTypeID from this list. Tenants
// configure their own set — mus looks the depositor's chosen name (from
// --type, default "Data") up here on each invocation, cached for the batch.
type SampleType struct {
	ID   int64  `json:"sampleTypeID"`
	Name string `json:"name"`
}

// ListSampleTypes returns the tenant's configured sample types. Used by
// FindSampleTypeByName to resolve a human-readable name to the numeric ID
// eLabNext's create-sample endpoint requires.
func (c *Client) ListSampleTypes() ([]SampleType, error) {
	// eLabNext paginates; ask for the widest reasonable page. Tenants
	// have dozens of sampleTypes at most, not thousands.
	var out struct {
		Data       []SampleType `json:"data"`
		TotalRecs  int          `json:"totalRecords"`
		MaxRecs    int          `json:"maxRecords"`
	}
	params := url.Values{}
	params.Set("$pageSize", "500")
	if err := c.get("sampleTypes", params, &out); err != nil {
		return nil, fmt.Errorf("list sampleTypes: %w", err)
	}
	return out.Data, nil
}

// FindSampleTypeByName returns the sample type matching `name` (case-
// insensitive exact match). Returns nil + error when none matches; the
// error text lists what IS available so the depositor can pick.
func (c *Client) FindSampleTypeByName(name string) (*SampleType, error) {
	types, err := c.ListSampleTypes()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(name))
	for i := range types {
		if strings.ToLower(types[i].Name) == needle {
			return &types[i], nil
		}
	}
	// Not found — surface the full list so the depositor sees what's
	// possible without a second round-trip.
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, t.Name)
	}
	return nil, fmt.Errorf(
		"no sample type named %q on this ELN tenant. Available: %s",
		name, strings.Join(names, ", "))
}

// CreateSampleReq is the payload for POST /samples. Only sampleTypeID +
// name are hard-required by eLabNext; Description and Note are used by
// mus to record the depositor's `-m` message and the file's provenance
// block (path / sha256 / size).
type CreateSampleReq struct {
	SampleTypeID int64  `json:"sampleTypeID"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Note         string `json:"note,omitempty"`
	AltID        string `json:"altID,omitempty"`
}

// Sample is the response shape from POST /samples and GET /samples/{id}.
// Only sampleID is load-bearing for mus — the rest we echo back to the
// depositor via the summary. Archived is populated by GetSample and
// tells `mus eln sample` to treat a prior sidecar-recorded sample as
// stale (create a fresh one on re-run) rather than blindly reusing it.
type Sample struct {
	ID           int64  `json:"sampleID"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Note         string `json:"note"`
	SampleTypeID int64  `json:"sampleTypeID"`
	Archived     bool   `json:"archived"`
}

// GetSample fetches sample metadata by ID. Returns the sample record —
// including the Archived flag — or an *HTTPError with StatusCode 404
// when the sample does not exist. Used by `mus eln sample` to verify
// that a sidecar-recorded sample is still active before skipping
// re-registration.
//
// Per eLabNext docs: "Archived samples remain queryable via GET
// /samples/{id} and the archived-aware list endpoints, but no longer
// appear in regular listings." So this endpoint IS the right check —
// list-based queries default to archived=false and would spuriously
// return "not found" for archived-but-existent samples.
func (c *Client) GetSample(id int64) (*Sample, error) {
	var out Sample
	if err := c.get(fmt.Sprintf("samples/%d", id), nil, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 {
		// Defensive: some tenants have been observed returning 200
		// with an empty body for missing IDs instead of 404. Treat
		// that as not-found so the caller re-creates.
		return nil, &HTTPError{StatusCode: 404, Body: "empty sample response"}
	}
	return &out, nil
}

// IsNotFound reports whether err is an *HTTPError with a 404 status.
// Small helper for the common lookaside-cache pattern where we want to
// re-create a resource that has disappeared from the ELN.
func IsNotFound(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.StatusCode == 404
	}
	return false
}

// CreateSample creates one sample. Returns the new sampleID.
//
// The response body is either the sample object OR a bare integer
// (older tenants); we handle both by trying integer-parse first.
func (c *Client) CreateSample(req CreateSampleReq) (*Sample, error) {
	if req.SampleTypeID == 0 {
		return nil, errors.New("CreateSample: SampleTypeID is required")
	}
	if req.Name == "" {
		return nil, errors.New("CreateSample: Name is required")
	}
	// Try full-object response first — that's what modern eLabNext
	// returns.
	var full Sample
	if err := c.post("samples", req, &full); err == nil && full.ID != 0 {
		return &full, nil
	}
	// Fallback: bare integer sampleID. Older tenants returned this.
	var id int64
	if err := c.post("samples", req, &id); err != nil {
		return nil, fmt.Errorf("create sample: %w", err)
	}
	if id == 0 {
		return nil, errors.New("create sample: empty response")
	}
	return &Sample{ID: id, Name: req.Name, SampleTypeID: req.SampleTypeID}, nil
}

// CreateExperimentSection appends a section of the given type to the
// experiment. Common types: TEXT, FILE, COMMENT, SAMPLESIN, SAMPLESOUT.
// Returns the section's expJournalID (used by
// LinkSamplesToSection / UploadFile).
//
// This is the more general form of the existing CreateCommentSection /
// CreateFileSection helpers — kept alongside for backward compat.
func (c *Client) CreateExperimentSection(expID int64, sectionType, header string) (int64, error) {
	expID = FixExperimentID(expID)
	body := map[string]string{
		"sectionType":   sectionType,
		"sectionHeader": header,
	}
	var journalID int64
	if err := c.post(fmt.Sprintf("experiments/%d/sections", expID), body, &journalID); err != nil {
		return 0, fmt.Errorf("create %s section: %w", sectionType, err)
	}
	return journalID, nil
}

// LinkSamplesToSection attaches every sample in sampleIDs to a
// SAMPLESIN or SAMPLESOUT experiment section. The body is a JSON array
// of int sampleIDs — no wrapper object.
//
// eLabNext requires PUT (not POST) for this endpoint; POST returns
// HTTP 405. Confirmed against the live API and the developer docs
// (see developer.elabnext.com — samples on sections use PUT/DELETE).
func (c *Client) LinkSamplesToSection(expJournalID int64, sampleIDs []int64) error {
	if len(sampleIDs) == 0 {
		return nil
	}
	var ignored any
	if err := c.put(fmt.Sprintf("experiments/sections/%d/samples", expJournalID),
		sampleIDs, &ignored); err != nil {
		return fmt.Errorf("link %d samples to section %d: %w",
			len(sampleIDs), expJournalID, err)
	}
	return nil
}

// UploadFile uploads a local file to a FILE section. uploadName overrides the
// remote name; if empty, the base name of `path` is used.
func (c *Client) UploadFile(journalID int64, path, uploadName string) error {
	if uploadName == "" {
		uploadName = filepath.Base(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	pipeReader, pipeWriter := io.Pipe()
	mw := multipart.NewWriter(pipeWriter)
	errCh := make(chan error, 1)
	go func() {
		defer pipeWriter.Close()
		part, err := mw.CreateFormFile("file", uploadName)
		if err != nil {
			errCh <- err
			return
		}
		if _, err := io.Copy(part, f); err != nil {
			errCh <- err
			return
		}
		errCh <- mw.Close()
	}()

	u, err := c.buildURL(fmt.Sprintf("experiments/sections/%d/files", journalID),
		url.Values{"fileName": {uploadName}})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", u, pipeReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := <-errCh; err != nil {
		return err
	}
	return nil
}

// --- internal HTTP helpers ---------------------------------------------------

func (c *Client) buildURL(path string, params url.Values) (string, error) {
	u, err := url.Parse(c.BaseURL + "/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return "", err
	}
	if len(params) > 0 {
		u.RawQuery = params.Encode()
	}
	return u.String(), nil
}

func (c *Client) get(path string, params url.Values, out any) error {
	u, err := c.buildURL(path, params)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) post(path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u, err := c.buildURL(path, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", u, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) put(path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u, err := c.buildURL(path, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", u, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// HTTPError is returned by the eLabNext client on any non-2xx response.
// Callers that care about specific status codes (e.g. 404 for "sample
// deleted") can errors.As on this type instead of matching error text.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", c.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode/100 != 2 {
		return &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	// eLabJournal returns 2xx with an empty body for some state-mutating
	// endpoints (PUT /experiments/sections/<id>/content is one). Treat that
	// as success and leave `out` zero-valued — the caller would only have
	// gotten a discardable response anyway.
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
