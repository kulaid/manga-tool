package utils

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"manga-tool/internal/cache"
	"manga-tool/internal/komga"
)

// AppConfig holds the application configuration
type AppConfig struct {
	MangaBaseDir     string // Base directory for manga library (where processed files are stored)
	TempDir          string
	Port             string
	PromptTimeout    time.Duration
	Komga            komga.Config
	RealDebridAPIKey string
	Parallelism      int // Number of parallel workers for operations, configurable via PARALLELISM
}

// createRequiredDirectories creates all necessary directories for the application
func createRequiredDirectories(appConfig *AppConfig) error {
	// Create temp directory
	if err := os.MkdirAll(appConfig.TempDir, 0755); err != nil {
		return fmt.Errorf("failed to create temp directory %s: %v", appConfig.TempDir, err)
	}

	// Create cache directory (always at /config/cache)
	cacheDir := "/config/cache"
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory %s: %v", cacheDir, err)
	}

	// Create logs directory (always at /config/logs)
	logsDir := "/config/logs"
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("failed to create logs directory %s: %v", logsDir, err)
	}

	// Initialize cache system with the cache directory
	cache.Initialize(cacheDir)

	log.Printf("Created directories: temp=%s, cache=%s, logs=%s", appConfig.TempDir, cacheDir, logsDir)
	return nil
}

// Initialize initializes the application (no auth - handled by Authelia)
func Initialize(appConfig *AppConfig) (*template.Template, error) {
	// Create necessary directories automatically
	if err := createRequiredDirectories(appConfig); err != nil {
		return nil, fmt.Errorf("failed to create required directories: %v", err)
	}

	// Define template functions
	funcMap := template.FuncMap{
		"contains": strings.Contains,
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04:05")
		},
		"formatDuration": func(d time.Duration) string {
			return d.Round(time.Second).String()
		},
		"statusColor": func(status string) string {
			switch status {
			case "pending":
				return "secondary"
			case "active":
				return "primary"
			case "completed":
				return "success"
			case "failed":
				return "danger"
			case "cancelled":
				return "warning"
			default:
				return "info"
			}
		},
		"progressPercentage": func(progress, total int) int {
			if total <= 0 {
				return 0
			}
			return int((float64(progress) / float64(total)) * 100)
		},
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("invalid dict call")
			}
			dict := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
	}

	// Load templates
	templates, err := template.New("").Funcs(funcMap).ParseGlob("/opt/manga-tool/templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %v", err)
	}

	// Initialize cache
	cache.Initialize("/opt/manga-tool")

	// Override with environment variables if set
	appConfig.MangaBaseDir = GetEnv("MANGA_TOOL_MANGA_BASE_DIR", appConfig.MangaBaseDir)
	appConfig.MangaBaseDir = GetEnv("MANGA_TOOL_KOMGA_BASE_DIR", appConfig.MangaBaseDir)
	appConfig.TempDir = GetEnv("MANGA_TOOL_TEMP_DIR", appConfig.TempDir)
	appConfig.Port = GetEnv("MANGA_TOOL_PORT", appConfig.Port)
	if timeout := GetEnvInt("MANGA_TOOL_PROMPT_TIMEOUT", 0); timeout > 0 {
		appConfig.PromptTimeout = time.Duration(timeout) * time.Second
	}
	appConfig.Parallelism = GetEnvInt("MANGA_TOOL_PARALLELISM", appConfig.Parallelism)
	appConfig.Komga.URL = GetEnv("MANGA_TOOL_KOMGA_URL", appConfig.Komga.URL)
	appConfig.Komga.Username = GetEnv("MANGA_TOOL_KOMGA_USER", appConfig.Komga.Username)
	appConfig.Komga.Password = GetEnv("MANGA_TOOL_KOMGA_PASSWORD", appConfig.Komga.Password)

	// Ensure directories exist
	os.MkdirAll(appConfig.TempDir, 0755)

	// Load Komga config only if komga.json exists or environment variables are set
	// Default is NO Komga integration unless explicitly configured
	komgaConfigFile := "komga.json"
	komgaConfigExists := false

	// Check if komga.json exists in current directory
	if _, err := os.Stat(komgaConfigFile); err == nil {
		komgaConfigExists = true
	} else {
		// Try the absolute path
		komgaConfigFile = "/opt/manga-tool/komga.json"
		if _, err := os.Stat(komgaConfigFile); err == nil {
			komgaConfigExists = true
		}
	}

	// Load config from file if it exists
	if komgaConfigExists {
		komgaJSON, err := os.ReadFile(komgaConfigFile)
		if err != nil {
			return nil, fmt.Errorf("error reading Komga config: %v", err)
		}
		if err := json.Unmarshal(komgaJSON, &appConfig.Komga); err != nil {
			return nil, fmt.Errorf("error unmarshaling Komga config: %v", err)
		}
		log.Printf("Loaded Komga configuration from %s", komgaConfigFile)
	} else {
		// No config file - initialize empty/disabled config
		appConfig.Komga = komga.Config{
			URL:            "",
			Username:       "",
			Password:       "",
			Libraries:      []string{},
			RefreshEnabled: false,
		}
	}

	// Check for environment variables - these enable and override Komga config
	envURL := os.Getenv("KOMGA_URL")
	envUsername := os.Getenv("KOMGA_USERNAME")
	envPassword := os.Getenv("KOMGA_PASSWORD")

	// If any Komga env vars are set, enable Komga and use them
	if envURL != "" || envUsername != "" || envPassword != "" {
		if envURL != "" {
			log.Printf("Setting Komga URL from environment: %s", envURL)
			appConfig.Komga.URL = envURL
		}
		if envUsername != "" {
			log.Printf("Setting Komga Username from environment")
			appConfig.Komga.Username = envUsername
		}
		if envPassword != "" {
			log.Printf("Setting Komga Password from environment")
			appConfig.Komga.Password = envPassword
		}

		// Enable refresh if credentials are provided
		appConfig.Komga.RefreshEnabled = true
	}

	// Libraries configuration
	if envLibraries := os.Getenv("KOMGA_LIBRARIES"); envLibraries != "" {
		log.Printf("Setting Komga Libraries from environment")
		appConfig.Komga.Libraries = strings.Split(envLibraries, ",")
	}

	// Refresh enabled override
	if envRefreshEnabled := os.Getenv("KOMGA_REFRESH_ENABLED"); envRefreshEnabled != "" {
		log.Printf("Setting Komga RefreshEnabled from environment: %s", envRefreshEnabled)
		appConfig.Komga.RefreshEnabled = envRefreshEnabled == "true"
	}

	// Log final Komga status
	if appConfig.Komga.URL != "" && appConfig.Komga.RefreshEnabled {
		log.Printf("Komga integration enabled: %s", appConfig.Komga.URL)
	} else {
		log.Printf("Komga integration disabled (no configuration found)")
	}

	// Set parallelism from environment variable, default to 2
	appConfig.Parallelism = GetEnvInt("PARALLELISM", 2)
	log.Printf("Using parallelism setting: %d", appConfig.Parallelism)

	return templates, nil
}

// GetEnv gets an environment variable or returns a default value
func GetEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// GetEnvInt gets an environment variable as an integer or returns a default value
func GetEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		intValue, err := strconv.Atoi(value)
		if err == nil {
			return intValue
		}
	}
	return defaultValue
}

// SanitizeJSONString sanitizes a string for JSON use
func SanitizeJSONString(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = regexp.MustCompile(`[\x00-\x1F\x7F]`).ReplaceAllString(s, "")
	// Limit length to prevent huge logs
	if len(s) > 2000 {
		s = s[:2000] + "... (truncated)"
	}
	return s
}

// TruncateString truncates a string to a maximum length and adds ellipsis
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// FindCBZFilesFromMount finds all CBZ files in a mounted directory
func FindCBZFilesFromMount(dir string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	// Walk through the directory to find .cbz files
	for _, entry := range entries {
		if entry.IsDir() {
			// Recursively check subdirectories
			subFiles, err := FindCBZFilesFromMount(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			files = append(files, subFiles...)
		} else {
			// Check if file has .cbz extension
			if filepath.Ext(entry.Name()) == ".cbz" {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}

	return files, nil
}
