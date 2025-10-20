package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"manga-tool/cmd/manga-tool/utils"
	"manga-tool/internal/komga"
)

// parseLibrariesEnv parses comma-separated library names from environment variable
func parseLibrariesEnv(envValue string) []string {
	if envValue == "" {
		return []string{}
	}
	return strings.Split(envValue, ",")
}

// main is the entry point of the application
func main() {
	// Create default configuration
	appConfig := &utils.AppConfig{
		MangaBaseDir:     utils.GetEnv("MANGA_BASE_DIR", "/mnt/manga"),
		TempDir:          "/config/temp",
		Port:             utils.GetEnv("PORT", "25000"),
		PromptTimeout:    5 * time.Minute,
		RealDebridAPIKey: utils.GetEnv("REALDEBRID_API_KEY", ""),
		Komga: komga.Config{
			URL:            utils.GetEnv("KOMGA_URL", ""),
			Username:       utils.GetEnv("KOMGA_USERNAME", ""),
			Password:       utils.GetEnv("KOMGA_PASSWORD", ""),
			Libraries:      parseLibrariesEnv(utils.GetEnv("KOMGA_LIBRARIES", "")),
			RefreshEnabled: utils.GetEnv("KOMGA_REFRESH_ENABLED", "true") == "true",
		},
	}

	// Initialize application context
	ctx, err := NewAppContext(appConfig)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}

	// Log configuration settings
	log.Printf("Starting with configuration: MangaBaseDir=%s", appConfig.MangaBaseDir)

	// Set up routes
	r := mux.NewRouter()
	RegisterRoutes(r, ctx)

	// Start server
	log.Printf("Starting server on port %s", appConfig.Port)
	if err := http.ListenAndServe(":"+appConfig.Port, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
