package realdebrid

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	apiKey     string
	httpClient *http.Client
}

type TorrentInfo struct {
	ID       string   `json:"id"`
	Filename string   `json:"filename"`
	Status   string   `json:"status"`
	Progress float64  `json:"progress"`
	Speed    int64    `json:"speed"`
	Size     int64    `json:"size"`
	Links    []string `json:"links"`
}

type DownloadLink struct {
	Download string `json:"download"`
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) AddMagnet(magnetLink string) (*TorrentInfo, error) {
	apiURL := "https://api.real-debrid.com/rest/1.0/torrents/addMagnet"

	form := url.Values{}
	form.Add("magnet", magnetLink)
	body := strings.NewReader(form.Encode())

	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error response from Real-Debrid: %s", string(respBody))
	}

	var torrentInfo TorrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&torrentInfo); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	return &torrentInfo, nil
}

func (c *Client) GetTorrentInfo(torrentID string) (*TorrentInfo, error) {
	apiURL := fmt.Sprintf("https://api.real-debrid.com/rest/1.0/torrents/info/%s", torrentID)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error response from Real-Debrid: %s", string(body))
	}

	var torrentInfo TorrentInfo
	if err := json.NewDecoder(resp.Body).Decode(&torrentInfo); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	return &torrentInfo, nil
}

func (c *Client) GetDownloadLink(torrentID string) (string, error) {
	// First get the torrent info to check status and get links
	info, err := c.GetTorrentInfo(torrentID)
	if err != nil {
		return "", fmt.Errorf("failed to get torrent info: %v", err)
	}

	// Check if torrent is ready
	if info.Status != "downloaded" {
		return "", fmt.Errorf("torrent not ready (status: %s, progress: %.1f%%)", info.Status, info.Progress)
	}

	// Check if we have any links
	if len(info.Links) == 0 {
		return "", fmt.Errorf("no download links available in torrent info")
	}

	// Get the first link and unrestrict it
	link := info.Links[0]
	apiURL := "https://api.real-debrid.com/rest/1.0/unrestrict/link"
	form := url.Values{}
	form.Add("link", link)
	formBody := strings.NewReader(form.Encode())

	req, err := http.NewRequest("POST", apiURL, formBody)
	if err != nil {
		return "", fmt.Errorf("error creating unrestrict request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error making unrestrict request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading unrestrict response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error response from Real-Debrid unrestrict (status %d): %s", resp.StatusCode, string(respBody))
	}

	var unrestrictInfo struct {
		Download string `json:"download"`
	}
	if err := json.Unmarshal(respBody, &unrestrictInfo); err != nil {
		return "", fmt.Errorf("error decoding unrestrict response: %v", err)
	}

	if unrestrictInfo.Download == "" {
		return "", fmt.Errorf("no download link found in unrestrict response")
	}

	return unrestrictInfo.Download, nil
}

func (c *Client) SelectAllFiles(torrentID string) error {
	apiURL := fmt.Sprintf("https://api.real-debrid.com/rest/1.0/torrents/selectFiles/%s", torrentID)
	form := url.Values{}
	// Passing "all" tells Real-Debrid to automatically select all files.
	form.Add("files", "all")
	formBody := strings.NewReader(form.Encode())

	req, err := http.NewRequest("POST", apiURL, formBody)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	// 204 No Content is a valid success response for this endpoint
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	// For other status codes, read the response body for error details
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("error response from Real-Debrid (selectFiles) (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Check if the response indicates any issues
	var result struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &result); err == nil {
		if result.Status == "error" {
			return fmt.Errorf("Real-Debrid reported error: %s", result.Message)
		}
	}

	return nil
}

func IsMagnetLink(url string) bool {
	return len(url) > 8 && url[:8] == "magnet:?"
}
