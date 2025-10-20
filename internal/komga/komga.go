package komga

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Logger interface for handling logs
type Logger interface {
	Info(msg string)
	Warning(msg string)
	Error(msg string)
}

// Config holds Komga API configuration
type Config struct {
	URL            string
	Username       string
	Password       string
	Libraries      []string
	RefreshEnabled bool
	Logger         Logger
}

// Library represents a Komga library
type Library struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DefaultConfig creates a default Komga configuration
func DefaultConfig() *Config {
	return &Config{
		URL:            "http://localhost:25600",
		Username:       "",
		Password:       "",
		Libraries:      []string{},
		RefreshEnabled: true,
	}
}

// Client is a Komga API client
type Client struct {
	config *Config
	http   *http.Client
}

// NewClient creates a new Komga client
func NewClient(config *Config) *Client {
	return &Client{
		config: config,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetLibraries fetches all libraries from Komga
func (c *Client) GetLibraries() ([]Library, error) {
	if !c.config.RefreshEnabled || c.config.URL == "" || c.config.Username == "" || c.config.Password == "" {
		return nil, fmt.Errorf("komga not fully configured")
	}

	logger := c.config.Logger

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/libraries", c.config.URL), nil)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to create request: %v", err))
		}
		return nil, err
	}

	req.SetBasicAuth(c.config.Username, c.config.Password)

	resp, err := c.http.Do(req)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to get libraries: %v", err))
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to get libraries, status code: %d", resp.StatusCode))
		}
		return nil, fmt.Errorf("failed to get libraries, status code: %d", resp.StatusCode)
	}

	var libraries []Library
	if err := json.NewDecoder(resp.Body).Decode(&libraries); err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to decode libraries: %v", err))
		}
		return nil, err
	}

	return libraries, nil
}

// RefreshLibrary refreshes a specific library
func (c *Client) RefreshLibrary(libraryID string) error {
	if !c.config.RefreshEnabled || c.config.URL == "" || c.config.Username == "" || c.config.Password == "" {
		return fmt.Errorf("komga not fully configured")
	}

	logger := c.config.Logger

	// Make sure libraryID doesn't have extra whitespace
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return fmt.Errorf("library ID cannot be empty")
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/libraries/%s/scan", c.config.URL, libraryID), nil)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to create request for library %s: %v", libraryID, err))
		}
		return err
	}

	req.SetBasicAuth(c.config.Username, c.config.Password)

	resp, err := c.http.Do(req)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to refresh library %s: %v", libraryID, err))
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to refresh library %s, status code: %d", libraryID, resp.StatusCode))
		}
		return fmt.Errorf("failed to refresh library %s, status code: %d", libraryID, resp.StatusCode)
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Successfully refreshed library %s", libraryID))
	}

	return nil
}

// RefreshAllLibraries refreshes all configured libraries
func (c *Client) RefreshAllLibraries() bool {
	if !c.config.RefreshEnabled || c.config.URL == "" || c.config.Username == "" || c.config.Password == "" {
		if c.config.Logger != nil {
			c.config.Logger.Warning("Komga refresh not enabled or not fully configured")
		}
		return false
	}

	logger := c.config.Logger

	// Get all available libraries
	allLibraries, err := c.GetLibraries()
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to get libraries: %v", err))
		}
		return false
	}

	// Build a map of library names to IDs for easy lookup
	libraryMap := make(map[string]string)
	for _, lib := range allLibraries {
		libraryMap[lib.Name] = lib.ID
		libraryMap[lib.ID] = lib.ID // Also map ID to ID for direct ID usage
	}

	var librariesToRefresh []string

	// If specific libraries are configured, resolve them to IDs
	if len(c.config.Libraries) > 0 && !(len(c.config.Libraries) == 1 && strings.TrimSpace(c.config.Libraries[0]) == "") {
		for _, libNameOrID := range c.config.Libraries {
			libNameOrID = strings.TrimSpace(libNameOrID)
			if libNameOrID == "" {
				continue
			}

			// Try to resolve library name to ID, or use as ID if already an ID
			if libID, exists := libraryMap[libNameOrID]; exists {
				librariesToRefresh = append(librariesToRefresh, libID)
			} else {
				// Not found - log warning but don't fail
				if logger != nil {
					logger.Warning(fmt.Sprintf("Library '%s' not found in Komga - skipping", libNameOrID))
				}
			}
		}
	} else {
		// No specific libraries configured - refresh all
		for _, lib := range allLibraries {
			librariesToRefresh = append(librariesToRefresh, lib.ID)
		}
	}

	if len(librariesToRefresh) == 0 {
		if logger != nil {
			logger.Warning("No libraries to refresh")
		}
		return false
	}

	// Refresh each library
	successCount := 0
	for _, libraryID := range librariesToRefresh {
		if err := c.RefreshLibrary(libraryID); err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Failed to refresh library %s: %v", libraryID, err))
			}
		} else {
			successCount++
		}
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Refreshed %d/%d libraries", successCount, len(librariesToRefresh)))
	}

	return successCount > 0
}
