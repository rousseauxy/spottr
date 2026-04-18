package sabnzbd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/spottr/spottr/internal/config"
)

// Client wraps the SABnzbd HTTP API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// QueueItem represents one item in the SABnzbd download queue.
type QueueItem struct {
	ID       string  `json:"nzo_id"`
	Filename string  `json:"filename"`
	Status   string  `json:"status"`
	SizeMB   float64 `json:"mb"`
	LeftMB   float64 `json:"mbleft"`
	Percent  string  `json:"percentage"`
	Category string  `json:"cat"`
}

// QueueResponse is the response from the queue API call.
type QueueResponse struct {
	Queue struct {
		Status    string      `json:"status"`
		SpeedKB   string      `json:"kbpersec"`
		SizeMB    string      `json:"mb"`
		LeftMB    string      `json:"mbleft"`
		Items     []QueueItem `json:"slots"`
		PageItems int         `json:"noofslots"`
	} `json:"queue"`
}

// AddNZBResponse is returned after adding an NZB.
type AddNZBResponse struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids"`
}

// New creates a SABnzbd client from configuration.
func New(cfg *config.Config) *Client {
	scheme := "http"
	if cfg.SABTLS {
		scheme = "https"
	}
	return &Client{
		baseURL: fmt.Sprintf("%s://%s:%d/sabnzbd/api", scheme, cfg.SABHost, cfg.SABPort),
		apiKey:  cfg.SABAPIKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Ping checks connectivity to SABnzbd. Returns nil if reachable.
func (c *Client) Ping() error {
	params := c.baseParams()
	params.Set("mode", "version")
	resp, err := c.get(params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sabnzbd ping: status %d", resp.StatusCode)
	}
	return nil
}

// AddNZBByURL sends a URL to SABnzbd for download.
func (c *Client) AddNZBByURL(nzbURL, name, category string) ([]string, error) {
	params := c.baseParams()
	params.Set("mode", "addurl")
	params.Set("name", nzbURL)
	params.Set("nzbname", name)
	if category != "" {
		params.Set("cat", category)
	}

	resp, err := c.get(params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result AddNZBResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode addurl response: %w", err)
	}
	if !result.Status {
		return nil, fmt.Errorf("sabnzbd addurl failed")
	}
	return result.NzoIDs, nil
}

// AddNZBContent sends raw NZB content directly to SABnzbd.
func (c *Client) AddNZBContent(nzbContent []byte, name, category string) ([]string, error) {
	params := c.baseParams()
	params.Set("mode", "addfile")
	params.Set("nzbname", name)
	if category != "" {
		params.Set("cat", category)
	}

	// Use multipart form upload
	endpoint := c.baseURL + "?" + params.Encode()
	body, contentType, err := buildMultipart("nzbfile", name+".nzb", nzbContent)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(endpoint, contentType, body)
	if err != nil {
		return nil, fmt.Errorf("post nzb: %w", err)
	}
	defer resp.Body.Close()

	var result AddNZBResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode addfile response: %w", err)
	}
	if !result.Status {
		return nil, fmt.Errorf("sabnzbd addfile failed")
	}
	return result.NzoIDs, nil
}

// GetQueue returns the current download queue.
func (c *Client) GetQueue(start, limit int) (*QueueResponse, error) {
	params := c.baseParams()
	params.Set("mode", "queue")
	params.Set("start", fmt.Sprintf("%d", start))
	params.Set("limit", fmt.Sprintf("%d", limit))

	resp, err := c.get(params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result QueueResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode queue: %w", err)
	}
	return &result, nil
}

func (c *Client) baseParams() url.Values {
	v := url.Values{}
	v.Set("apikey", c.apiKey)
	v.Set("output", "json")
	return v
}

func (c *Client) get(params url.Values) (*http.Response, error) {
	endpoint := c.baseURL + "?" + params.Encode()
	resp, err := c.httpClient.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("sabnzbd request: %w", err)
	}
	return resp, nil
}

// buildMultipart creates a multipart body for file upload.
func buildMultipart(fieldName, filename string, content []byte) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", err
	}
	w.Close()
	return &buf, w.FormDataContentType(), nil
}
