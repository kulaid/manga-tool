package handlers

import (
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
	"manga-tool/internal/util"
)

// ProcessFormData holds the form data for manga processing
type ProcessFormData struct {
	MangaTitle       string
	MangaReaderURL   string
	MangaDexURL      string
	DownloadURL      string
	IsOneshot        bool
	DeleteOriginals  bool
	AsFolder         bool
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
		IsOneshot:        r.FormValue("is_oneshot") == "true",
		DeleteOriginals:  r.FormValue("delete_originals") == "true",
		AsFolder:         r.FormValue("as_folder") == "true",
		Language:         r.FormValue("language"),
		IsUpdateMetadata: false,
	}

	// Get uploaded files
	files := r.MultipartForm.File["manga_files"]

	// Save all inputted values to cache IMMEDIATELY after form submission
	if mangaTitle != "" {
		if err := cache.SaveSources(mangaTitle, formData.MangaReaderURL, formData.MangaDexURL, formData.DownloadURL, formData.IsOneshot); err != nil {
			h.Logger("WARNING", fmt.Sprintf("Failed to save values to cache: %v", err))
		} else {
			h.Logger("INFO", fmt.Sprintf("Saved manga values to cache immediately for: %s", mangaTitle))
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

	// Set up process cancellation
	cancelChan, forceCancelChan := setupProcessCancellation(proc)

	// Prepare threadData for metadata update
	threadData := map[string]interface{}{
		"manga_title":     proc.Title,
		"update_metadata": true,
	}

	appConfig := h.Config

	go processors.ProcessManga(
		threadData,
		cancelChan,
		forceCancelChan,
		convertToProcessorsAppConfig(appConfig, proc.ID, h.Logger),
		h.ProcessManager,
		proc.ID,
		nil, // webInputFunc not needed for metadata update
		h.Logger,
		nil, // initializePromptManager not needed for metadata update
	)
}

// startMangaProcessing starts the main manga processing workflow
func (h *MangaHandler) startMangaProcessing(proc *internal.Process, formData ProcessFormData) {
	h.Logger("INFO", fmt.Sprintf("Starting manga processing: %s for %s", proc.ID, proc.Title))

	// Set up process cancellation
	cancelChan, forceCancelChan := setupProcessCancellation(proc)

	// Initialize the prompt manager with the new process
	initializePromptManager := createPromptManagerInitializer(h.Logger)
	if h.PromptManager != nil {
		h.PromptManager.SetProcess(proc)
	}

	// Get safe WebInput function
	webInputFunc := getWebInputFunc(h.WebInput, h.Logger)

	// Prepare data for the processor
	threadData := map[string]interface{}{
		"manga_title":      formData.MangaTitle,
		"mangareader_url":  formData.MangaReaderURL,
		"mangadex_url":     formData.MangaDexURL,
		"download_url":     formData.DownloadURL,
		"is_manga":         true,
		"is_oneshot":       formData.IsOneshot,
		"delete_originals": formData.DeleteOriginals,
		"language":         formData.Language,
	}

	// Get config from app config
	appConfig := h.Config

	// Start manga processing in a goroutine
	go processors.ProcessManga(
		threadData,
		cancelChan,
		forceCancelChan,
		convertToProcessorsAppConfig(appConfig, proc.ID, h.Logger),
		h.ProcessManager,
		proc.ID,
		webInputFunc,
		h.Logger,
		initializePromptManager,
	)
}

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
		respondJSON(w, map[string]interface{}{
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

	renderTemplate(w, h.Templates, "process_details.html", data, h.Logger)
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
	renderTemplate(w, h.Templates, "index.html", data, h.Logger)
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
	renderTemplate(w, h.Templates, "processes.html", data, h.Logger)
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
	respondJSONSuccess(w, nil)
}

// copyWithProgress copies a file with progress reporting
func copyWithProgress(dst io.Writer, src io.Reader, size int64) (int64, error) {
	// Use a 32KB buffer for efficient copying
	bufSize := 32 * 1024
	buf := make([]byte, bufSize)

	var written int64
	var lastReported int64
	reportInterval := int64(1024 * 1024) // Report every 1MB

	for {
		nr, err := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}

			// Report progress at intervals
			if size > 0 && written-lastReported >= reportInterval {
				lastReported = written
				// Progress reporting is handled by caller's logger
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return written, err
		}
	}

	return written, nil
}
