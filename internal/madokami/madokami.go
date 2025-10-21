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
