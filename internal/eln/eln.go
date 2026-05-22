// Package eln is a minimal eLabJournal REST client.
//
// Authentication: the API key is sent as the `Authorization` header value
// (yes, raw, no `Bearer` prefix — that's how eLabJournal does it). Base URL
// and key are loaded from the secret store (`eln_url`, `eln_apikey`).
package eln

import (
	"bytes"
	"encoding/json"
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

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", c.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	return dec.Decode(out)
}
