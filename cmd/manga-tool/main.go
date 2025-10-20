package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"

	"manga-tool/cmd/manga-tool/utils"
)

// main is the entry point of the application
func main() {
	// Create default configuration
	appConfig := &utils.AppConfig{
		MangaBaseDir:     utils.GetEnv("MANGA_BASE_DIR", "data/manga"),
		TempDir:          os.TempDir(),
		Port:             "25000",
		PromptTimeout:    5 * time.Minute,
		RealDebridAPIKey: utils.GetEnv("REALDEBRID_API_KEY", ""),
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
