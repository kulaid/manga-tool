package madokami

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Client struct {
	username   string
	password   string
	httpClient *http.Client
	cookieJar  *cookiejar.Jar
	loggedIn   bool
	mu         sync.Mutex
}

var (
	globalClient *Client
	clientMu     sync.Mutex
)

// GetClient returns the singleton Madokami client instance
func GetClient(username, password string) (*Client, error) {
	clientMu.Lock()
	defer clientMu.Unlock()

	// If client exists and credentials match, return it
	if globalClient != nil {
		if globalClient.username == username && globalClient.password == password {
			return globalClient, nil
		}
		// Credentials changed, create new client
		globalClient = nil
	}

	// Create new client
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %v", err)
	}

	globalClient = &Client{
		username: username,
		password: password,
		httpClient: &http.Client{
			Jar: jar,
		},
		cookieJar: jar,
		loggedIn:  false,
	}

	// Try to load cached cookies (use internal version since we don't need double lock)
	globalClient.mu.Lock()
	if err := globalClient.loadCookiesInternal(); err == nil {
		globalClient.loggedIn = true
	}
	globalClient.mu.Unlock()

	return globalClient, nil
}

// Login authenticates with Madokami and stores the session cookie
func (c *Client) Login() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already logged in, skip
	if c.loggedIn {
		return nil
	}

	if c.username == "" || c.password == "" {
		return fmt.Errorf("username and password are required")
	}

	// Madokami login endpoint
	loginURL := "https://madokami.al/auth/login"

	// Prepare form data
	formData := url.Values{}
	formData.Set("username", c.username)
	formData.Set("password", c.password)

	// Create request
	req, err := http.NewRequest("POST", loginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create login request: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://madokami.al/auth/login")

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read response body for debugging
	body, _ := io.ReadAll(resp.Body)

	// Check for cookies as indication of successful login
	madokamiURL, _ := url.Parse("https://madokami.al")
	cookies := c.cookieJar.Cookies(madokamiURL)

	if len(cookies) > 0 {
		// Check if we got a session cookie
		for _, cookie := range cookies {
			if cookie.Name == "PHPSESSID" || cookie.Name == "session" || cookie.Name == "madokami_session" {
				c.loggedIn = true
				// Save cookies to cache (without lock, already locked in Login)
				_ = c.saveCookiesInternal()
				return nil
			}
		}
		// If we got any cookies after login, assume success
		c.loggedIn = true
		// Save cookies to cache (without lock, already locked in Login)
		_ = c.saveCookiesInternal()
		return nil
	}

	// Check if login was successful by status code
	// Madokami returns 302 redirect on successful login
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusSeeOther {
		c.loggedIn = true
		// Save cookies to cache (without lock, already locked in Login)
		_ = c.saveCookiesInternal()
		return nil
	}

	return fmt.Errorf("login failed (status %d, cookies: %d): %s", resp.StatusCode, len(cookies), string(body))
}

// saveCookiesInternal saves cookies to disk cache (must be called with lock held)
func (c *Client) saveCookiesInternal() error {
	cacheDir := "/config/cache"
	cookieFile := filepath.Join(cacheDir, "madokami_cookies.json")

	madokamiURL, _ := url.Parse("https://madokami.al")
	cookies := c.cookieJar.Cookies(madokamiURL)

	data, err := json.Marshal(cookies)
	if err != nil {
		return fmt.Errorf("failed to marshal cookies: %v", err)
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %v", err)
	}

	if err := os.WriteFile(cookieFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write cookie file: %v", err)
	}

	return nil
}

// loadCookiesInternal loads cookies from disk cache (must be called with lock held)
func (c *Client) loadCookiesInternal() error {
	cookieFile := filepath.Join("/config/cache", "madokami_cookies.json")

	data, err := os.ReadFile(cookieFile)
	if err != nil {
		return fmt.Errorf("failed to read cookie file: %v", err)
	}

	var cookies []*http.Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return fmt.Errorf("failed to unmarshal cookies: %v", err)
	}

	madokamiURL, _ := url.Parse("https://madokami.al")
	c.cookieJar.SetCookies(madokamiURL, cookies)

	return nil
}

// GetCookieString returns the cookies as a string for wget
func (c *Client) GetCookieString() string {
	if !c.loggedIn {
		return ""
	}

	madokamiURL, _ := url.Parse("https://madokami.al")
	cookies := c.cookieJar.Cookies(madokamiURL)

	var cookieStrings []string
	for _, cookie := range cookies {
		cookieStrings = append(cookieStrings, fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
	}

	return strings.Join(cookieStrings, "; ")
}

// IsLoggedIn returns whether the client has a valid session
func (c *Client) IsLoggedIn() bool {
	return c.loggedIn
}

// IsMadokamiURL checks if a URL is a Madokami URL
func IsMadokamiURL(urlStr string) bool {
	return strings.Contains(urlStr, "madokami.al")
}

// GetFolderFiles fetches the list of file URLs from a Madokami folder
func (c *Client) GetFolderFiles(folderURL string) ([]string, error) {
	if !c.loggedIn {
		return nil, fmt.Errorf("not logged in to Madokami")
	}

	// Fetch the folder page
	req, err := http.NewRequest("GET", folderURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch folder: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	// Parse HTML to extract download links
	// Look for: <a href="/Manga/..." rel="nofollow">filename.cbz</a>
	// Pattern: href="/Manga/...something.cbz" or .zip or .rar
	pattern := regexp.MustCompile(`<a href="(/[^"]+\.(?:cbz|zip|rar|cbr))"[^>]*rel="nofollow"`)
	matches := pattern.FindAllStringSubmatch(string(body), -1)

	var fileURLs []string
	baseURL := "https://madokami.al"

	for _, match := range matches {
		if len(match) > 1 {
			filePath := match[1]
			fullURL := baseURL + filePath
			fileURLs = append(fileURLs, fullURL)
		}
	}

	return fileURLs, nil
}

// DownloadFile downloads a single file with authentication using 3 parallel chunks
func (c *Client) DownloadFile(fileURL, destDir string) error {
	if !c.loggedIn {
		return fmt.Errorf("not logged in to Madokami")
	}

	// Extract filename from URL
	parsedURL, _ := url.Parse(fileURL)
	filename := filepath.Base(parsedURL.Path)

	// Decode URL-encoded filename
	filename, _ = url.QueryUnescape(filename)

	// Create destination file path
	destPath := filepath.Join(destDir, filename)

	// First, get the file size with a HEAD request
	headReq, err := http.NewRequest("HEAD", fileURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create HEAD request: %v", err)
	}
	headReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	headResp, err := c.httpClient.Do(headReq)
	if err != nil {
		return fmt.Errorf("failed to get file info: %v", err)
	}
	headResp.Body.Close()

	// Check if server supports range requests
	acceptRanges := headResp.Header.Get("Accept-Ranges")
	contentLength := headResp.ContentLength

	if acceptRanges != "bytes" || contentLength <= 0 {
		// Server doesn't support range requests, fall back to single download
		return c.downloadFileSingle(fileURL, destPath)
	}

	// Download file in 3 chunks
	return c.downloadFileChunked(fileURL, destPath, contentLength)
}

// downloadFileSingle downloads a file without chunking
func (c *Client) downloadFileSingle(fileURL, destPath string) error {
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download file: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}

	return nil
}

// downloadFileChunked downloads a file in 3 parallel chunks
func (c *Client) downloadFileChunked(fileURL, destPath string, totalSize int64) error {
	const numChunks = 3

	// Calculate chunk size
	chunkSize := totalSize / numChunks

	// Create temporary files for each chunk
	tempFiles := make([]string, numChunks)
	for i := 0; i < numChunks; i++ {
		tempFiles[i] = fmt.Sprintf("%s.part%d", destPath, i)
	}

	// Download chunks in parallel
	var wg sync.WaitGroup
	errChan := make(chan error, numChunks)

	for i := 0; i < numChunks; i++ {
		wg.Add(1)

		go func(chunkIndex int) {
			defer wg.Done()

			// Calculate byte range for this chunk
			start := int64(chunkIndex) * chunkSize
			end := start + chunkSize - 1

			// Last chunk gets any remaining bytes
			if chunkIndex == numChunks-1 {
				end = totalSize - 1
			}

			// Download this chunk
			if err := c.downloadChunk(fileURL, tempFiles[chunkIndex], start, end); err != nil {
				errChan <- fmt.Errorf("chunk %d failed: %v", chunkIndex, err)
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	if len(errChan) > 0 {
		// Clean up temp files
		for _, tempFile := range tempFiles {
			os.Remove(tempFile)
		}
		return <-errChan
	}

	// Merge chunks into final file
	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer outFile.Close()

	for i := 0; i < numChunks; i++ {
		chunkFile, err := os.Open(tempFiles[i])
		if err != nil {
			return fmt.Errorf("failed to open chunk %d: %v", i, err)
		}

		_, err = io.Copy(outFile, chunkFile)
		chunkFile.Close()

		if err != nil {
			return fmt.Errorf("failed to merge chunk %d: %v", i, err)
		}

		// Delete temp file after merging
		os.Remove(tempFiles[i])
	}

	return nil
}

// downloadChunk downloads a specific byte range of a file
func (c *Client) downloadChunk(fileURL, destPath string, start, end int64) error {
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download chunk: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create chunk file: %v", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write chunk: %v", err)
	}

	return nil
}
