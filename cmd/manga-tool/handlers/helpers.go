package handlers

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"strings"

	"manga-tool/cmd/manga-tool/processors"
	"manga-tool/cmd/manga-tool/utils"
	"manga-tool/internal"
	"manga-tool/internal/komga"
	"manga-tool/internal/util"
)

// setupProcessCancellation creates cancel channels and sets up cancel functions for a process
func setupProcessCancellation(proc *internal.Process) (chan struct{}, chan struct{}) {
	cancelChan := make(chan struct{})
	forceCancelChan := make(chan struct{})

	proc.CancelFunc = func() { close(cancelChan) }
	proc.ForceCancel = func() { close(forceCancelChan) }

	return cancelChan, forceCancelChan
}

// createKomgaClient creates a Komga client with configuration from AppConfig or environment variables
func createKomgaClient(config *utils.AppConfig, processID string, logger func(level, message string)) *komga.Client {
	// Get Komga configuration from AppConfig or environment
	komgaURL := config.Komga.URL
	komgaUsername := config.Komga.Username
	komgaPassword := config.Komga.Password
	komgaRefreshEnabled := config.Komga.RefreshEnabled

	// Read from environment if empty
	if komgaURL == "" {
		komgaURL = os.Getenv("KOMGA_URL")
	}
	if komgaUsername == "" {
		komgaUsername = os.Getenv("KOMGA_USERNAME")
	}
	if komgaPassword == "" {
		komgaPassword = os.Getenv("KOMGA_PASSWORD")
	}

	// Get libraries from environment or use configured ones
	var libraries []string
	komgaLibraries := os.Getenv("KOMGA_LIBRARIES")
	if komgaLibraries != "" {
		libraries = strings.Split(komgaLibraries, ",")
	} else if len(config.Komga.Libraries) > 0 {
		libraries = config.Komga.Libraries
	}

	// Filter out empty libraries
	filteredLibraries := make([]string, 0)
	for _, lib := range libraries {
		if strings.TrimSpace(lib) != "" {
			filteredLibraries = append(filteredLibraries, lib)
		}
	}

	logger("INFO", "Komga refresh configuration: URL="+komgaURL+", Username="+komgaUsername)

	return komga.NewClient(&komga.Config{
		URL:            komgaURL,
		Username:       komgaUsername,
		Password:       komgaPassword,
		Libraries:      filteredLibraries,
		RefreshEnabled: komgaRefreshEnabled,
		Logger:         util.NewSimpleLogger(processID, logger),
	})
}

// convertToProcessorsAppConfig converts utils.AppConfig to processors.AppConfig
func convertToProcessorsAppConfig(config *utils.AppConfig, processID string, logger func(level, message string)) processors.AppConfig {
	return processors.AppConfig{
		MangaBaseDir:     config.MangaBaseDir,
		TempDir:          config.TempDir,
		PromptTimeout:    config.PromptTimeout,
		RealDebridAPIKey: config.RealDebridAPIKey,
		Komga: komga.Config{
			URL:            config.Komga.URL,
			Username:       config.Komga.Username,
			Password:       config.Komga.Password,
			Libraries:      config.Komga.Libraries,
			RefreshEnabled: config.Komga.RefreshEnabled,
			Logger:         util.NewSimpleLogger(processID, logger),
		},
		Parallelism: config.Parallelism,
	}
}

// getWebInputFunc returns a safe WebInput function
func getWebInputFunc(webInput func(processID, prompt, inputType string) string, logger func(level, message string)) func(processID, prompt, inputType string) string {
	if webInput == nil {
		return func(processID, prompt, inputType string) string {
			logger("WARNING", "WebInput function not provided, using empty response")
			return ""
		}
	}
	return webInput
}

// createPromptManagerInitializer creates a standard prompt manager initializer function
func createPromptManagerInitializer(logger func(level, message string)) func(process *internal.Process) {
	return func(process *internal.Process) {
		logger("INFO", "Prompt manager initialized for process: "+process.ID)
	}
}

// respondJSON sends a JSON response with the given data
func respondJSON(w http.ResponseWriter, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(data)
}

// respondJSONError sends a JSON error response
func respondJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"error":   message,
	})
}

// respondJSONSuccess sends a JSON success response
func respondJSONSuccess(w http.ResponseWriter, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}
	data["success"] = true
	respondJSON(w, data)
}

// renderTemplate renders a template with error handling and logging
func renderTemplate(w http.ResponseWriter, templates *template.Template, 
	templateName string, data interface{}, logger func(level, message string)) {
	if err := templates.ExecuteTemplate(w, templateName, data); err != nil {
		logger("ERROR", "Error rendering template "+templateName+": "+err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
