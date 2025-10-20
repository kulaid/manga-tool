package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/mux"

	"manga-tool/cmd/manga-tool/processors"
	"manga-tool/cmd/manga-tool/utils"
	"manga-tool/internal"
	"manga-tool/internal/cache"
	"manga-tool/internal/komga"
	"manga-tool/internal/util"
)

// ProcessFormData holds the form data for manga processing
type ProcessFormData struct {
	MangaTitle       string
	MangaReaderURL   string
	MangaDexURL      string
	DownloadURL      string
	DownloadUsername string
	DownloadPassword string
	IsOneshot        bool
	DeleteOriginals  bool
	Language         string
	IsUpdateMetadata bool
}

// MangaHandler contains dependencies for manga handlers
// No auth - using Authelia for authentication
type MangaHandler struct {
	Config         *utils.AppConfig
	ProcessManager *internal.ProcessManager
	Logger         func(level, message string)
	CurrentProcess *string
	Templates      *template.Template
	WebInput       func(processID, prompt, inputType string) string
	PromptManager  *util.PromptManager
}

// UploadHandler handles manga upload requests
func (h *MangaHandler) UploadHandler(w http.ResponseWriter, r *http.Request) {
	// Parse form (max 100MB)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, fmt.Sprintf("Error parsing form: %v", err), http.StatusBadRequest)
		return
	}

	// Get form values
	mangaTitle := r.FormValue("manga_title")
	if mangaTitle == "" {
		http.Error(w, "Manga title is required", http.StatusBadRequest)
		return
	}

	// Get all form values
	formData := ProcessFormData{
		MangaTitle:       mangaTitle,
		MangaReaderURL:   r.FormValue("mangareader_url"),
		MangaDexURL:      r.FormValue("mangadex_url"),
		DownloadURL:      r.FormValue("download_url"),
		DownloadUsername: r.FormValue("download_username"),
		DownloadPassword: r.FormValue("download_password"),
		IsOneshot:        r.FormValue("is_oneshot") == "true",
		DeleteOriginals:  r.FormValue("delete_originals") == "true",
		Language:         r.FormValue("language"),
		IsUpdateMetadata: false,
	}

	// Get uploaded files
	files := r.MultipartForm.File["manga_files"]

	// Save manga source URLs to cache for future use
	if mangaTitle != "" && (formData.MangaReaderURL != "" || formData.MangaDexURL != "") {
		if err := cache.SaveSources(mangaTitle, formData.MangaReaderURL, formData.MangaDexURL); err != nil {
			h.Logger("WARNING", fmt.Sprintf("Failed to save source URLs to cache: %v", err))
		} else {
			h.Logger("INFO", fmt.Sprintf("Saved manga source URLs to cache for: %s", mangaTitle))
		}
	}

	// Save download URL to cache if provided
	if mangaTitle != "" && formData.DownloadURL != "" {
		if err := cache.SaveSourceWithDownload(mangaTitle, formData.MangaReaderURL, formData.MangaDexURL, formData.DownloadURL); err != nil {
			h.Logger("WARNING", fmt.Sprintf("Failed to save download URL to cache: %v", err))
		} else {
			h.Logger("INFO", fmt.Sprintf("Saved download URL to cache for: %s", mangaTitle))
		}
	}

	// Clean and create upload directory
	uploadDir := filepath.Join(h.Config.TempDir, "manga_uploads")
	os.RemoveAll(uploadDir)
	os.MkdirAll(uploadDir, 0755)

	// Save uploaded files
	for _, fileHeader := range files {
		if fileHeader.Filename == "" {
			continue
		}

		// Save each uploaded file
		file, err := fileHeader.Open()
		if err != nil {
			h.Logger("ERROR", fmt.Sprintf("Error opening uploaded file: %v", err))
			http.Error(w, fmt.Sprintf("Error opening uploaded file: %v", err), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		// Create new file on disk
		dst, err := os.Create(filepath.Join(uploadDir, fileHeader.Filename))
		if err != nil {
			h.Logger("ERROR", fmt.Sprintf("Error creating file: %v", err))
			http.Error(w, fmt.Sprintf("Error creating file: %v", err), http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		// Copy uploaded file to disk
		if _, err := copyWithProgress(dst, file, fileHeader.Size); err != nil {
			h.Logger("ERROR", fmt.Sprintf("Error copying file: %v", err))
			http.Error(w, fmt.Sprintf("Error copying file: %v", err), http.StatusInternalServerError)
			return
		}

		h.Logger("INFO", fmt.Sprintf("Uploaded file saved: %s (%d bytes)", fileHeader.Filename, fileHeader.Size))
	}

	// Create a pending process to store form data
	// This replaces the session-based state management
	proc := h.ProcessManager.NewProcess(internal.ProcessTypeProcess, mangaTitle)
	proc.Metadata = map[string]interface{}{
		"form_data": formData,
		"pending":   true, // Mark as pending until ProcessHandler starts it
	}
	h.Logger("INFO", fmt.Sprintf("Created pending process %s for %s", proc.ID, mangaTitle))

	// Redirect to process page with the process ID
	http.Redirect(w, r, fmt.Sprintf("/process?id=%s", proc.ID), http.StatusSeeOther)
}

// ProcessHandler handles the /process endpoint - starting new processes
// Now uses ProcessManager metadata instead of sessions for state management
func (h *MangaHandler) ProcessHandler(w http.ResponseWriter, r *http.Request) {
	// Get process ID from query parameter
	processID := r.URL.Query().Get("id")
	if processID == "" {
		http.Error(w, "Process ID is required", http.StatusBadRequest)
		return
	}

	// Get the process from ProcessManager
	proc, exists := h.ProcessManager.GetProcess(processID)
	if !exists {
		http.Error(w, "Process not found", http.StatusNotFound)
		return
	}

	// Extract form data from metadata
	formDataRaw, ok := proc.Metadata["form_data"]
	if !ok {
		http.Error(w, "Process metadata missing", http.StatusInternalServerError)
		return
	}

	// Type assert to get the form data
	formData, ok := formDataRaw.(ProcessFormData)
	if !ok {
		http.Error(w, "Invalid process metadata format", http.StatusInternalServerError)
		return
	}

	// Check if this is a pending process or if we're reconnecting to an existing one
	isPending, _ := proc.Metadata["pending"].(bool)

	// If it's already running, just show the process page
	if !isPending && proc.Status == internal.ProcessStatusRunning {
		h.Logger("INFO", fmt.Sprintf("Reconnecting to existing process: %s", processID))
		data := map[string]interface{}{
			"manga_title":     proc.Title,
			"update_metadata": formData.IsUpdateMetadata,
			"ProcessID":       processID,
			"CurrentYear":     time.Now().Year(),
		}
		h.Templates.ExecuteTemplate(w, "process.html", data)
		return
	}

	// If pending, start the actual process
	if isPending {
		// Remove pending flag
		delete(proc.Metadata, "pending")

		// Set the global currentProcessID if CurrentProcess is provided
		if h.CurrentProcess != nil {
			*h.CurrentProcess = processID
		}

		// Handle different process types
		if formData.IsUpdateMetadata {
			h.startMetadataUpdateProcess(proc, formData)
		} else {
			h.startMangaProcessing(proc, formData)
		}
	}

	// Render the process template
	data := map[string]interface{}{
		"manga_title":     proc.Title,
		"update_metadata": formData.IsUpdateMetadata,
		"ProcessID":       processID,
		"CurrentYear":     time.Now().Year(),
	}
	h.Templates.ExecuteTemplate(w, "process.html", data)
}

// startMetadataUpdateProcess starts a metadata update process
func (h *MangaHandler) startMetadataUpdateProcess(proc *internal.Process, _ ProcessFormData) {
	h.Logger("INFO", fmt.Sprintf("Starting metadata update process: %s for %s", proc.ID, proc.Title))

	// Create cancel channels
	cancelChan := make(chan struct{})
	forceCancelChan := make(chan struct{})

	// Set up cancel functions
	proc.CancelFunc = func() { close(cancelChan) }
	proc.ForceCancel = func() { close(forceCancelChan) }

	// Start metadata update in a goroutine
	go func() {
		h.Logger("INFO", fmt.Sprintf("Starting metadata update for %s", proc.Title))

		// Mock update progress (replace with actual metadata update logic)
		for i := 0; i <= 5; i++ {
			select {
			case <-cancelChan:
				return
			default:
				proc.Update(i, 5, fmt.Sprintf("Updating metadata: %d%%", i*20))
				time.Sleep(500 * time.Millisecond)
			}
		}

		proc.Update(5, 5, "Metadata update completed")
		h.ProcessManager.CompleteProcess(proc.ID)
	}()
}

// startMangaProcessing starts the main manga processing workflow
func (h *MangaHandler) startMangaProcessing(proc *internal.Process, formData ProcessFormData) {
	h.Logger("INFO", fmt.Sprintf("Starting manga processing: %s for %s", proc.ID, proc.Title))

	// Create cancel channels
	cancelChan := make(chan struct{})
	forceCancelChan := make(chan struct{})

	// Set up cancel functions
	proc.CancelFunc = func() { close(cancelChan) }
	proc.ForceCancel = func() { close(forceCancelChan) }

	// Initialize the prompt manager with the new process
	initializePromptManager := func(process *internal.Process) {
		if h.PromptManager != nil {
			h.PromptManager.SetProcess(process)
		}
		h.Logger("INFO", fmt.Sprintf("Prompt manager initialized for process: %s", process.ID))
	}

	// Use the WebInput function
	webInputFunc := h.WebInput
	if webInputFunc == nil {
		webInputFunc = func(processID, prompt, inputType string) string {
			h.Logger("WARNING", "WebInput function not provided, using empty response")
			return ""
		}
	}

	// Prepare data for the processor
	threadData := map[string]interface{}{
		"manga_title":       formData.MangaTitle,
		"mangareader_url":   formData.MangaReaderURL,
		"mangadex_url":      formData.MangaDexURL,
		"download_url":      formData.DownloadURL,
		"download_username": formData.DownloadUsername,
		"download_password": formData.DownloadPassword,
		"is_manga":          true,
		"is_oneshot":        formData.IsOneshot,
		"delete_originals":  formData.DeleteOriginals,
		"language":          formData.Language,
	}

	// Get config from app config
	appConfig := h.Config

	// Start manga processing in a goroutine
	go processors.ProcessManga(
		threadData,
		cancelChan,
		forceCancelChan,
		processors.AppConfig{
			MangaBaseDir:     appConfig.MangaBaseDir,
			TempDir:          appConfig.TempDir,
			PromptTimeout:    appConfig.PromptTimeout,
			RealDebridAPIKey: appConfig.RealDebridAPIKey,
			Komga: komga.Config{
				URL:            appConfig.Komga.URL,
				Username:       appConfig.Komga.Username,
				Password:       appConfig.Komga.Password,
				Libraries:      appConfig.Komga.Libraries,
				RefreshEnabled: appConfig.Komga.RefreshEnabled,
				Logger:         util.NewSimpleLogger(proc.ID, h.Logger),
			},
		},
		h.ProcessManager,
		proc.ID,
		webInputFunc,
		h.Logger,
		initializePromptManager,
	)
}

// Old ProcessHandler code removed - replaced with metadata-based approach
/*
	// Check if we already have a process ID stored in the session
	storedProcessID, hasStoredID := session.Values["process_id"].(string)

	// Store the current process ID in a common variable
	var currentProcessID string

	// First try to recover the existing process if we have a stored ID
	if hasStoredID && storedProcessID != "" {
		h.Logger("INFO", fmt.Sprintf("Found stored process ID in session: %s", storedProcessID))

		// Check if this process still exists and is active
		existingProcess, exists := h.ProcessManager.GetProcess(storedProcessID)
		if exists && existingProcess.Status == internal.ProcessStatusRunning {
			h.Logger("INFO", fmt.Sprintf("Reconnecting to existing process: %s", storedProcessID))
			currentProcessID = storedProcessID

			// Render the process template with the existing process
			data := map[string]interface{}{
				"manga_title":     mangaTitle,
				"update_metadata": isUpdateMetadata,
				"ProcessID":       currentProcessID,
				"CurrentYear":     time.Now().Year(),
				"Flashes":         GetFlashes(w, r, h.SessionStore),
			}

			h.Templates.ExecuteTemplate(w, "process.html", data)
			return
		}
	}

	// If we're here, we need to create a new process
	var proc *internal.Process

	if isWarmCache {
		// Create a new process for cache warming
		proc = h.ProcessManager.NewProcess(internal.ProcessTypeCacheWarm, mangaTitle)
		currentProcessID = proc.ID
		// Set the global currentProcessID if CurrentProcess is provided
		if h.CurrentProcess != nil {
			*h.CurrentProcess = currentProcessID
		}
		h.Logger("INFO", fmt.Sprintf("Created new cache warming process: %s", proc.ID))

		// Create cancel channels
		cancelChan := make(chan struct{})
		forceCancelChan := make(chan struct{})

		// Set up cancel functions
		proc.CancelFunc = func() { close(cancelChan) }
		proc.ForceCancel = func() { close(forceCancelChan) }

		// Start cache warming in a goroutine
		go func() {
			// Should call the actual cache warming function here
			h.Logger("INFO", fmt.Sprintf("Starting cache warming for %s", mangaTitle))

			// Mock warming progress
			for i := 0; i <= 10; i++ {
				select {
				case <-cancelChan:
					return
				default:
					proc.Update(i, 10, fmt.Sprintf("Warming cache: %d%%", i*10))
					time.Sleep(500 * time.Millisecond)
				}
			}

			proc.Update(10, 10, "Cache warming completed")
			h.ProcessManager.CompleteProcess(proc.ID)
		}()
	} else if isUpdateMetadata {
		// Create a new process for metadata update
		proc = h.ProcessManager.NewProcess(internal.ProcessTypeMetadataUpdate, mangaTitle)
		currentProcessID = proc.ID
		// Set the global currentProcessID if CurrentProcess is provided
		if h.CurrentProcess != nil {
			*h.CurrentProcess = currentProcessID
		}
		h.Logger("INFO", fmt.Sprintf("Created new metadata update process: %s", proc.ID))

		// Create cancel channels
		cancelChan := make(chan struct{})
		forceCancelChan := make(chan struct{})

		// Set up cancel functions
		proc.CancelFunc = func() { close(cancelChan) }
		proc.ForceCancel = func() { close(forceCancelChan) }

		// Start metadata update in a goroutine
		go func() {
			// Should call the actual metadata update function here
			h.Logger("INFO", fmt.Sprintf("Starting metadata update for %s", mangaTitle))

			// Mock update progress
			for i := 0; i <= 5; i++ {
				select {
				case <-cancelChan:
					return
				default:
					proc.Update(i, 5, fmt.Sprintf("Updating metadata: %d%%", i*20))
					time.Sleep(500 * time.Millisecond)
				}
			}

			proc.Update(5, 5, "Metadata update completed")
			h.ProcessManager.CompleteProcess(proc.ID)
		}()
	} else {
		// Regular manga processing
		proc = h.ProcessManager.NewProcess(internal.ProcessTypeProcess, mangaTitle)
		currentProcessID = proc.ID
		// Set the global currentProcessID if CurrentProcess is provided
		if h.CurrentProcess != nil {
			*h.CurrentProcess = currentProcessID
		}
		h.Logger("INFO", fmt.Sprintf("Created new manga processing process: %s", proc.ID))

		// Extract data from session
		mangareaderURL, _ := session.Values["mangareader_url"].(string)
		mangadexURL, _ := session.Values["mangadex_url"].(string)
		downloadURL, _ := session.Values["download_url"].(string)
		downloadUsername, _ := session.Values["download_username"].(string)
		downloadPassword, _ := session.Values["download_password"].(string)
		isOneshot, _ := session.Values["is_oneshot"].(bool)
		deleteOriginals, _ := session.Values["delete_originals"].(bool)
		language, _ := session.Values["language"].(string)

		// Create cancel channels
		cancelChan := make(chan struct{})
		forceCancelChan := make(chan struct{})

		// Set up cancel functions
		proc.CancelFunc = func() { close(cancelChan) }
		proc.ForceCancel = func() { close(forceCancelChan) }

		// Initialize the prompt manager with the new process
		initializePromptManager := func(process *internal.Process) {
			// Connect the prompt manager to this process
			if h.PromptManager != nil {
				h.PromptManager.SetProcess(process)
			}
			h.Logger("INFO", fmt.Sprintf("Prompt manager initialized for process: %s", process.ID))
		}

		// Use the WebInput function directly since ProcessManga expects the new signature
		webInputFunc := h.WebInput
		if webInputFunc == nil {
			webInputFunc = func(processID, prompt, inputType string) string {
				h.Logger("WARNING", "WebInput function not provided, using empty response")
				return ""
			}
		}

		// Prepare data for the processor
		threadData := map[string]interface{}{
			"manga_title":       mangaTitle,
			"mangareader_url":   mangareaderURL,
			"mangadex_url":      mangadexURL,
			"download_url":      downloadURL,
			"download_username": downloadUsername,
			"download_password": downloadPassword,
			"is_manga":          isManga,
			"is_oneshot":        isOneshot,
			"delete_originals":  deleteOriginals,
			"language":          language,
		}

		// Get config from app config (assuming it's passed in the handler initialization)
		appConfig := h.Config

		// Start manga processing in a goroutine
		go processors.ProcessManga(
			threadData,
			cancelChan,
			forceCancelChan,
			processors.AppConfig{
				MangaBaseDir:     appConfig.MangaBaseDir,
				TempDir:          appConfig.TempDir,
				MangaBaseDir:     appConfig.MangaBaseDir,
				PromptTimeout:    appConfig.PromptTimeout,
				RealDebridAPIKey: appConfig.RealDebridAPIKey,
				Komga: komga.Config{
					URL:            appConfig.Komga.URL,
					Username:       appConfig.Komga.Username,
					Password:       appConfig.Komga.Password,
					Libraries:      appConfig.Komga.Libraries,
					RefreshEnabled: appConfig.Komga.RefreshEnabled,
					Logger:         &processors.WebLogger{LogFunc: h.Logger},
				},
			},
			h.ProcessManager,
			currentProcessID,
			webInputFunc,
			h.Logger,
			initializePromptManager,
		)
	}

	// Store the process ID in the session for page refreshes
	session.Values["process_id"] = currentProcessID
	session.Save(r, w)

	// Render the process template
	data := map[string]interface{}{
		"manga_title":     mangaTitle,
		"update_metadata": isUpdateMetadata,
		"ProcessID":       currentProcessID,
		"CurrentYear":     time.Now().Year(),
		"Flashes":         GetFlashes(w, r, h.SessionStore),
	}

	h.Templates.ExecuteTemplate(w, "process.html", data)
*/

// ProcessDetailsHandler shows details for a specific process
func (h *MangaHandler) ProcessDetailsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	processID := vars["id"]

	proc, exists := h.ProcessManager.GetProcess(processID)
	if !exists {
		http.Error(w, "Process not found", http.StatusNotFound)
		return
	}

	// Return details JSON for AJAX requests
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":                  proc.ID,
			"type":                proc.Type,
			"title":               proc.Title,
			"status":              proc.Status,
			"progress":            proc.Progress,
			"total":               proc.Total,
			"progress_percentage": proc.ProgressPercentage(),
			"message":             proc.Message,
			"error":               proc.Error,
			"start_time":          proc.StartTime,
			"end_time":            proc.EndTime,
		})
		return
	}

	// For regular requests, render the process details page
	data := map[string]interface{}{
		"Process":     proc,
		"CurrentYear": time.Now().Year(),
	}

	h.Templates.ExecuteTemplate(w, "process_details.html", data)
}

// ProcessCancelHandler cancels a process
func (h *MangaHandler) ProcessCancelHandler(w http.ResponseWriter, r *http.Request) {
	// Get process ID from URL
	vars := mux.Vars(r)
	processID := vars["id"]

	// Cancel process using the ProcessManager
	success := h.ProcessManager.CancelProcess(processID)
	if !success {
		http.Error(w, "Process not found or not running", http.StatusNotFound)
		return
	}

	h.Logger("INFO", fmt.Sprintf("Process %s cancelled", processID))

	// Redirect back to processes page
	http.Redirect(w, r, "/processes", http.StatusSeeOther)
}

// IndexHandler renders the main upload page
func (h *MangaHandler) IndexHandler(w http.ResponseWriter, r *http.Request) {
	// Create data for template
	data := map[string]interface{}{
		"CurrentYear": time.Now().Year(),
	}

	// Render template
	if err := h.Templates.ExecuteTemplate(w, "index.html", data); err != nil {
		h.Logger("ERROR", fmt.Sprintf("Error rendering template: %v", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ProcessesHandler renders the processes page
func (h *MangaHandler) ProcessesHandler(w http.ResponseWriter, r *http.Request) {
	// Get all processes for display
	allProcesses := h.ProcessManager.ListProcesses()

	// Create data for template
	data := map[string]interface{}{
		"Processes":   allProcesses,
		"CurrentYear": time.Now().Year(),
	}

	// Render template
	if err := h.Templates.ExecuteTemplate(w, "processes.html", data); err != nil {
		h.Logger("ERROR", fmt.Sprintf("Error rendering template: %v", err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// SubmitInputHandler handles user input submissions
func (h *MangaHandler) SubmitInputHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing form", http.StatusBadRequest)
		return
	}

	// Get input from form
	input := r.FormValue("input")
	promptID := r.FormValue("prompt_id")

	if promptID == "" {
		http.Error(w, "Prompt ID required", http.StatusBadRequest)
		return
	}

	// Send input to the prompt manager (this would need to be modified based on your implementation)
	// This is a stub that would need to be replaced
	h.Logger("INFO", "Input submitted: "+input+" for prompt ID: "+promptID)

	// Return success
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// copyWithProgress copies a file with progress reporting
func copyWithProgress(dst io.Writer, src io.Reader, size int64) (int64, error) {
	// Simple implementation without progress
	// TODO: Add progress reporting using the size parameter
	_ = size // Unused for now
	return io.Copy(dst, src)
}
