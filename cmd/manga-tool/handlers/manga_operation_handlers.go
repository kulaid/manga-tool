package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"manga-tool/cmd/manga-tool/processors"
	"manga-tool/cmd/manga-tool/utils"
	"manga-tool/internal"
	"manga-tool/internal/cache"
	"manga-tool/internal/util"
)

// MangaOperationHandler handles manga operations like updating metadata and deleting
// No auth - using Authelia for authentication
type MangaOperationHandler struct {
	Config         *utils.AppConfig
	Templates      *template.Template
	Logger         func(level, message string)
	ProcessManager *internal.ProcessManager
	UserPrompts    *map[string]chan string
	InputLock      *sync.Mutex
	WebInput       func(processID, prompt, inputType string) string
}

// FileSelectionHandler handles selecting files for processing
func (h *MangaOperationHandler) FileSelectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing form data", http.StatusBadRequest)
		return
	}

	// Get selected files
	selectedFiles := r.Form["files"]
	if len(selectedFiles) == 0 {
		http.Error(w, "No files selected", http.StatusBadRequest)
		return
	}

	// Return success
	respondJSONSuccess(w, nil)
}

// ChapterTitlesHandler handles chapter title updates
func (h *MangaOperationHandler) ChapterTitlesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing form data", http.StatusBadRequest)
		return
	}

	// Get chapter titles
	var chapterTitles map[string]string
	if err := json.NewDecoder(r.Body).Decode(&chapterTitles); err != nil {
		http.Error(w, "Error parsing chapter titles", http.StatusBadRequest)
		return
	}

	// Return success
	respondJSONSuccess(w, nil)
}

// SourceSelectionHandler handles source selection for manga
func (h *MangaOperationHandler) SourceSelectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error parsing form data", http.StatusBadRequest)
		return
	}

	// Get selected source
	source := r.FormValue("source")
	if source == "" {
		http.Error(w, "No source selected", http.StatusBadRequest)
		return
	}

	// Return success
	respondJSONSuccess(w, nil)
}

// PromptResponseHandler handles responses to prompts
func (h *MangaOperationHandler) PromptResponseHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse JSON body
	var requestData struct {
		Response string `json:"response"`
	}

	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		h.Logger("ERROR", fmt.Sprintf("Error parsing prompt response: %v", err))
		http.Error(w, "Error parsing request body", http.StatusBadRequest)
		return
	}

	response := requestData.Response
	h.Logger("INFO", fmt.Sprintf("Received prompt response: %s", response))

	// Find the active process that's waiting for input
	processes := h.ProcessManager.ListProcesses()
	var targetProcess *internal.Process

	for _, proc := range processes {
		if proc.IsWaiting {
			targetProcess = proc
			h.Logger("INFO", fmt.Sprintf("Found waiting process: %s", proc.ID))
			break
		}
	}

	if targetProcess == nil {
		h.Logger("WARNING", "No waiting process found for prompt response")
		respondJSONError(w, http.StatusBadRequest, "No waiting process found")
		return
	}

	// Submit the response to the appropriate channel
	// The WebInput function creates channels in UserPrompts map with unique IDs
	// We need to find the right channel for this process
	h.InputLock.Lock()
	var delivered bool
	for promptID, inputChan := range *h.UserPrompts {
		// Check if this prompt belongs to the target process
		// The promptID format is "processID-timestamp"
		if len(promptID) > len(targetProcess.ID) && promptID[:len(targetProcess.ID)] == targetProcess.ID {
			select {
			case inputChan <- response:
				h.Logger("INFO", fmt.Sprintf("Response delivered to process %s via prompt %s", targetProcess.ID, promptID))
				delivered = true
			default:
				h.Logger("WARNING", "Failed to deliver response - channel full or closed")
			}
			break
		}
	}
	h.InputLock.Unlock()

	if !delivered {
		h.Logger("WARNING", fmt.Sprintf("Could not find input channel for process %s", targetProcess.ID))
	}

	// Return success
	respondJSONSuccess(w, nil)
}

// ChaptersAPIHandler returns chapters for a manga
func (h *MangaOperationHandler) ChaptersAPIHandler(w http.ResponseWriter, r *http.Request) {
	// Get manga title from query parameters
	title := r.URL.Query().Get("title")
	if title == "" {
		http.Error(w, "Manga title is required", http.StatusBadRequest)
		return
	}

	// Try to get cached sources from internal/cache
	h.Logger("INFO", fmt.Sprintf("Looking up cached sources for manga: %s", title))

	// Import and use internal/cache to get cached sources and chapter titles
	type ChapterResponse struct {
		ID     string  `json:"id"`
		Number float64 `json:"number"`
		Title  string  `json:"title"`
		URL    string  `json:"url"`
		Source string  `json:"source"`
	}

	// Get all cached sources for API output
	var chapters []ChapterResponse

	// Try to get cached sources from the cache
	cachedSources, err := cache.GetCachedSources(title)
	if err == nil {
		// If we have cached sources, return them
		if cachedSources.MangaReader != "" {
			chapters = append(chapters, ChapterResponse{
				ID:     "mr-source",
				Number: 0,
				Title:  "MangaReader Source",
				URL:    cachedSources.MangaReader,
				Source: "mangareader",
			})
		}

		if cachedSources.MangaDex != "" {
			chapters = append(chapters, ChapterResponse{
				ID:     "md-source",
				Number: 0,
				Title:  "MangaDex Source",
				URL:    cachedSources.MangaDex,
				Source: "mangadex",
			})
		}

		if cachedSources.DownloadURL != "" {
			chapters = append(chapters, ChapterResponse{
				ID:     "download-url",
				Number: 0,
				Title:  "Download Link",
				URL:    cachedSources.DownloadURL,
				Source: "download",
			})
		}
	}

	// Response structure that includes oneshot information
	type ChaptersAPIResponse struct {
		Chapters  []ChapterResponse `json:"chapters"`
		IsOneshot bool              `json:"isOneshot"`
	}

	// Get oneshot status from cached sources
	isOneshot := false
	if err == nil {
		isOneshot = cachedSources.IsOneshot
	}

	// Return chapters and oneshot status as JSON
	response := ChaptersAPIResponse{
		Chapters:  chapters,
		IsOneshot: isOneshot,
	}

	respondJSON(w, response)
}

// ManageMangaHandler renders the manage manga page
func (h *MangaOperationHandler) ManageMangaHandler(w http.ResponseWriter, r *http.Request) {
	// Get all manga in the manga directory
	mangaTitles, err := utils.GetMangaTitles(h.Config.MangaBaseDir)
	if err != nil {
		h.Logger("ERROR", fmt.Sprintf("Error reading manga directory %s: %v", h.Config.MangaBaseDir, err))
		// Still render the template but with empty titles list and a flash message
		mangaTitles = []string{} // Use empty array instead of nil to avoid template errors
	}

	// Debug logs
	h.Logger("INFO", fmt.Sprintf("Manga directory: %s, found %d titles", h.Config.MangaBaseDir, len(mangaTitles)))

	// Sort titles alphabetically
	utils.SortStringSlice(mangaTitles)

	// Render template
	data := map[string]interface{}{
		"Title":       "Manage Manga",
		"Titles":      mangaTitles,
		"CurrentYear": time.Now().Year(),
	}

	renderTemplate(w, h.Templates, "manage_manga.html", data, h.Logger)
}

// UpdateMetadataHandler updates metadata for a manga
func (h *MangaOperationHandler) UpdateMetadataHandler(w http.ResponseWriter, r *http.Request) {
	// Get manga title from URL
	vars := mux.Vars(r)
	mangaTitle := vars["title"]
	if mangaTitle == "" {
		http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
		return
	}

	if r.Method == "POST" {
		// Get form values for scraping sources
		mangareaderURL := r.FormValue("mangareader_url")
		mangadexURL := r.FormValue("mangadex_url")
		isOneshot := r.FormValue("is_oneshot") == "true"
		asFolder := r.FormValue("as_folder") == "true"

		// Save all inputted values to cache IMMEDIATELY after form submission
		// Pass empty string for downloadURL to preserve existing value in cache
		if mangaTitle != "" {
			if err := cache.SaveSources(mangaTitle, mangareaderURL, mangadexURL, "", isOneshot); err != nil {
				h.Logger("WARNING", fmt.Sprintf("Failed to save values to cache: %v", err))
			} else {
				h.Logger("INFO", fmt.Sprintf("Saved manga values to cache immediately for: %s", mangaTitle))
			}
		}

		// Start a process to update metadata
		proc := h.ProcessManager.NewProcess(internal.ProcessTypeMetadataUpdate, mangaTitle)
		h.Logger("INFO", fmt.Sprintf("Starting metadata update for %s (Process ID: %s)", mangaTitle, proc.ID))

		// Initialize metadata map if nil
		if proc.Metadata == nil {
			proc.Metadata = make(map[string]interface{})
		}

		// Store metadata in process for later use
		proc.Metadata["mangareader_url"] = mangareaderURL
		proc.Metadata["mangadex_url"] = mangadexURL

		// Set up process cancellation
		cancelChan, forceCancelChan := setupProcessCancellation(proc)

		// Initialize the prompt manager
		initializePromptManager := createPromptManagerInitializer(h.Logger)

		// Get safe WebInput function
		webInputFunc := getWebInputFunc(h.WebInput, h.Logger)

		// Prepare data for ProcessManga - similar to normal processing but with update_metadata flag
		threadData := map[string]interface{}{
			"manga_title":      mangaTitle,
			"mangareader_url":  mangareaderURL,
			"mangadex_url":     mangadexURL,
			"download_url":     "", // No download for metadata update
			"is_manga":         true,
			"is_oneshot":       isOneshot,
			"delete_originals": false,
			"language":         "en",
			"update_metadata":  true, // Flag to indicate this is a metadata update
			"as_folder":        asFolder,
		}

		// Start metadata update using ProcessManga in a goroutine
		go processors.ProcessManga(
			threadData,
			cancelChan,
			forceCancelChan,
			convertToProcessorsAppConfig(h.Config, proc.ID, h.Logger),
			h.ProcessManager,
			proc.ID,
			webInputFunc,
			h.Logger,
			initializePromptManager,
		)

		// Return just the process ID as JSON for AJAX requests
		if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
			respondJSON(w, map[string]string{"process_id": proc.ID})
			return
		}

		// Render the process page to show live updates
		data := map[string]interface{}{
			"manga_title":     mangaTitle,
			"update_metadata": true,
			"ProcessID":       proc.ID,
			"CurrentYear":     time.Now().Year(),
		}

		renderTemplate(w, h.Templates, "process.html", data, h.Logger)
		return
	}

	// For GET requests, find CBZ files and show the update form
	mangaPath := filepath.Join(h.Config.MangaBaseDir, mangaTitle)
	files, err := util.FindCBZFilesFromMount(mangaPath)
	if err != nil {
		h.Logger("ERROR", fmt.Sprintf("Error finding CBZ files: %v", err))
		http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
		return
	}

	// Sort files by volume/chapter number (natural order)
	type fileWithNum struct {
		Index   int
		Path    string
		Name    string
		Size    string
		VolNum  float64
		ChapNum float64
	}
	var filesWithNum []fileWithNum
	for i, file := range files {
		fileInfo, err := os.Stat(file)
		var size string
		if err == nil {
			sizeKB := fileInfo.Size() / 1024
			if sizeKB > 1024 {
				sizeMB := float64(sizeKB) / 1024
				size = fmt.Sprintf("%.1f MB", sizeMB)
			} else {
				size = fmt.Sprintf("%d KB", sizeKB)
			}
		} else {
			size = "Unknown"
		}
		name := filepath.Base(file)
		vol := util.ExtractVolumeNumber(name)
		chap := util.ExtractChapterNumber(name)
		filesWithNum = append(filesWithNum, fileWithNum{
			Index:   i,
			Path:    file,
			Name:    name,
			Size:    size,
			VolNum:  vol,
			ChapNum: chap,
		})
	}
	// Sort: by chapter (if present), then volume, then name (to match delete files prompt)
	sort.SliceStable(filesWithNum, func(i, j int) bool {
		if filesWithNum[i].ChapNum >= 0 && filesWithNum[j].ChapNum >= 0 && filesWithNum[i].ChapNum != filesWithNum[j].ChapNum {
			return filesWithNum[i].ChapNum < filesWithNum[j].ChapNum
		}
		if filesWithNum[i].VolNum >= 0 && filesWithNum[j].VolNum >= 0 && filesWithNum[i].VolNum != filesWithNum[j].VolNum {
			return filesWithNum[i].VolNum < filesWithNum[j].VolNum
		}
		return filesWithNum[i].Name < filesWithNum[j].Name
	})
	var fileList []map[string]interface{}
	for idx, f := range filesWithNum {
		fileList = append(fileList, map[string]interface{}{
			"ID":   idx,
			"Name": f.Name,
			"Path": f.Path,
			"Size": f.Size,
		})
	}

	// Get cached source URLs if available
	var cachedMangaReader, cachedMangaDex string
	var cachedIsOneshot bool
	if cachedSources, err := cache.GetCachedSources(mangaTitle); err == nil {
		cachedMangaReader = cachedSources.MangaReader
		cachedMangaDex = cachedSources.MangaDex
		cachedIsOneshot = cachedSources.IsOneshot
	}

	// Render template for GET request
	data := map[string]interface{}{
		"Title":             mangaTitle,
		"MangaTitle":        mangaTitle,
		"Files":             fileList,
		"Year":              time.Now().Year(),
		"CachedMangaReader": cachedMangaReader,
		"CachedMangaDex":    cachedMangaDex,
		"CachedIsOneshot":   cachedIsOneshot,
	}

	renderTemplate(w, h.Templates, "update_metadata.html", data, h.Logger)
}

// DeleteMangaHandler renders the delete manga confirmation page
func (h *MangaOperationHandler) DeleteMangaHandler(w http.ResponseWriter, r *http.Request) {
	// Get manga title from URL
	vars := mux.Vars(r)
	mangaTitle := vars["title"]
	if mangaTitle == "" {
		http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
		return
	}

	// Redirect to confirmation page
	http.Redirect(w, r, "/confirm-delete/"+mangaTitle, http.StatusSeeOther)
}

// ConfirmDeleteHandler handles confirmation for manga deletion
func (h *MangaOperationHandler) ConfirmDeleteHandler(w http.ResponseWriter, r *http.Request) {
	// Get manga title from URL
	vars := mux.Vars(r)
	mangaTitle := vars["title"]
	if mangaTitle == "" {
		http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
		return
	}

	if r.Method == "POST" {
		// Get confirmation and deletion type
		confirm := r.FormValue("confirm") == "yes"
		deleteType := r.FormValue("delete_type")

		if !confirm {
			http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
			return
		}

		if deleteType == "files" {
			// Redirect to delete files page
			http.Redirect(w, r, "/delete-files/"+mangaTitle, http.StatusSeeOther)
			return
		}

		// Start a process to delete the manga
		proc := h.ProcessManager.NewProcess("delete", mangaTitle)
		h.Logger("INFO", fmt.Sprintf("Starting deletion for %s (Process ID: %s)", mangaTitle, proc.ID))

		// Start deletion in a goroutine
		go func() {
			// Process cleanup when finished
			defer func() {
				if r := recover(); r != nil {
					h.Logger("ERROR", fmt.Sprintf("Panic in manga deletion: %v", r))
					h.ProcessManager.FailProcess(proc.ID, fmt.Sprintf("Panic: %v", r))
				}
			}()

			// Get path
			mangaPath := filepath.Join(h.Config.MangaBaseDir, mangaTitle)

			h.Logger("INFO", fmt.Sprintf("Deleting manga directory: %s", mangaPath))
			proc.Update(0, 2, "Deleting manga directory")

			// Delete the directory using simple file operations
			if err := os.RemoveAll(mangaPath); err != nil {
				// Check if the error is just that the directory doesn't exist - which is not a real error
				if os.IsNotExist(err) {
					h.Logger("INFO", fmt.Sprintf("Directory %s already deleted", mangaPath))
				} else {
					// This is a real error
					h.Logger("ERROR", fmt.Sprintf("Error deleting directory: %v", err))
					h.ProcessManager.FailProcess(proc.ID, fmt.Sprintf("Error deleting directory: %v", err))
					return
				}
			} else {
				h.Logger("INFO", fmt.Sprintf("Successfully deleted directory: %s", mangaPath))
			}

			proc.Update(1, 2, "Deletion complete")

			// Verify deletion
			if _, err := os.Stat(mangaPath); err == nil {
				h.Logger("WARNING", "Directory still exists after deletion attempt")
			} else {
				h.Logger("INFO", "Verified directory has been removed from filesystem")
			}

			// Proceed to Komga refresh
			proc.Update(2, 2, "Refreshing Komga libraries")

			// Create Komga client and refresh libraries
			komgaClient := createKomgaClient(h.Config, proc.ID, h.Logger)

			if komgaClient.RefreshAllLibraries() {
				h.Logger("INFO", "Successfully refreshed Komga libraries")
			} else {
				h.Logger("WARNING", "Failed to refresh Komga libraries")
			}

			proc.Update(5, 5, "Deletion complete")
			h.ProcessManager.CompleteProcess(proc.ID)
		}()

		// Return just the process ID as JSON for AJAX requests
		if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
			respondJSON(w, map[string]string{"process_id": proc.ID})
			return
		}

		// Redirect to the processes page
		http.Redirect(w, r, "/processes", http.StatusSeeOther)
		return
	}

	// Get manga files and info for GET request
	mangaPath := filepath.Join(h.Config.MangaBaseDir, mangaTitle)

	// Get all files and calculate size
	files, err := h.getFiles(mangaPath)
	fileCount := len(files)
	totalSizeMB := 0.0

	if err != nil {
		h.Logger("WARNING", fmt.Sprintf("Error getting files for %s: %v", mangaTitle, err))
	} else {
		// Calculate total size
		for _, file := range files {
			filePath := filepath.Join(mangaPath, file)
			info, err := os.Stat(filePath)
			if err == nil {
				totalSizeMB += float64(info.Size()) / (1024 * 1024)
			}
		}
	}

	// Sort files by volume/chapter number (natural order)
	type fileWithNum struct {
		Name    string
		VolNum  float64
		ChapNum float64
	}
	var filesWithNum []fileWithNum
	for _, name := range files {
		vol := util.ExtractVolumeNumber(name)
		chap := util.ExtractChapterNumber(name)
		filesWithNum = append(filesWithNum, fileWithNum{
			Name:    name,
			VolNum:  vol,
			ChapNum: chap,
		})
	}
	sort.SliceStable(filesWithNum, func(i, j int) bool {
		if filesWithNum[i].ChapNum >= 0 && filesWithNum[j].ChapNum >= 0 && filesWithNum[i].ChapNum != filesWithNum[j].ChapNum {
			return filesWithNum[i].ChapNum < filesWithNum[j].ChapNum
		}
		if filesWithNum[i].VolNum >= 0 && filesWithNum[j].VolNum >= 0 && filesWithNum[i].VolNum != filesWithNum[j].VolNum {
			return filesWithNum[i].VolNum < filesWithNum[j].VolNum
		}
		return filesWithNum[i].Name < filesWithNum[j].Name
	})
	var sortedFiles []string
	for _, f := range filesWithNum {
		sortedFiles = append(sortedFiles, f.Name)
	}

	// Format the size with 2 decimal places
	sizeFormatted := fmt.Sprintf("%.2f", totalSizeMB)

	// Render template for GET request
	data := map[string]interface{}{
		"Title":       "Confirm Delete " + mangaTitle,
		"title":       mangaTitle,
		"MangaTitle":  mangaTitle,
		"file_count":  fileCount,
		"size_mb":     sizeFormatted,
		"files":       sortedFiles,
		"Year":        time.Now().Year(),
		"CurrentYear": time.Now().Year(),
	}

	renderTemplate(w, h.Templates, "confirm_delete.html", data, h.Logger)
}

// DeleteFilesHandler handles selective deletion of manga files
func (h *MangaOperationHandler) DeleteFilesHandler(w http.ResponseWriter, r *http.Request) {
	// Get manga title from URL
	vars := mux.Vars(r)
	mangaTitle := vars["title"]
	if mangaTitle == "" {
		http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
		return
	}

	// The manga directory under MangaBaseDir
	mangaPath := filepath.Join(h.Config.MangaBaseDir, mangaTitle)

	if r.Method == "POST" {
		// Parse form
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/delete-files/"+mangaTitle, http.StatusSeeOther)
			return
		}

		// Get selected files
		selectedFiles := r.Form["files"]
		if len(selectedFiles) == 0 {
			http.Redirect(w, r, "/delete-files/"+mangaTitle, http.StatusSeeOther)
			return
		}

		// Start a process to delete the files
		proc := h.ProcessManager.NewProcess("delete-files", mangaTitle)
		h.Logger("INFO", fmt.Sprintf("Starting file deletion for %s (Process ID: %s)", mangaTitle, proc.ID))

		// Start deletion in a goroutine
		go func() {
			// Process cleanup when finished
			defer func() {
				if r := recover(); r != nil {
					h.Logger("ERROR", fmt.Sprintf("Panic in file deletion: %v", r))
					h.ProcessManager.FailProcess(proc.ID, fmt.Sprintf("Panic: %v", r))
				}
			}()

			// Build a list of files for deletion
			var filesToDelete []string
			for _, file := range selectedFiles {
				filePath := filepath.Join(mangaPath, file)

				// Ensure filePath is under mangaPath for security
				if !strings.HasPrefix(filePath, mangaPath) {
					h.Logger("WARNING", fmt.Sprintf("Security check failed: %s is not in %s", filePath, mangaPath))
					continue
				}

				if _, err := os.Stat(filePath); err != nil {
					h.Logger("WARNING", fmt.Sprintf("Error getting file info for %s: %v", filePath, err))
					continue
				}

				filesToDelete = append(filesToDelete, filePath)
				h.Logger("INFO", fmt.Sprintf("Queueing for deletion: %s", filePath))
			}

			// Delete selected files
			proc.Update(0, len(filesToDelete)+1, "Deleting files")

			deletedCount := 0

			// Delete each file
			for i, filePath := range filesToDelete {
				if err := os.Remove(filePath); err != nil {
					h.Logger("WARNING", fmt.Sprintf("Error removing file %s: %v", filePath, err))
				} else {
					h.Logger("INFO", fmt.Sprintf("Deleted file: %s", filePath))
					deletedCount++
				}

				proc.Update(i+1, len(filesToDelete)+1, fmt.Sprintf("Deleted %d of %d files", i+1, len(filesToDelete)))
			}

			// Proceed to Komga refresh
			proc.Update(len(filesToDelete)+1, len(filesToDelete)+1, "Refreshing Komga libraries")

			// Create Komga client and refresh libraries
			komgaClient := createKomgaClient(h.Config, proc.ID, h.Logger)

			if komgaClient.RefreshAllLibraries() {
				h.Logger("INFO", "Successfully refreshed Komga libraries")
			} else {
				h.Logger("WARNING", "Failed to refresh Komga libraries")
			}

			// Report completion
			h.Logger("INFO", fmt.Sprintf("Successfully deleted %d files", deletedCount))

			h.ProcessManager.CompleteProcess(proc.ID)
		}()

		// Return just the process ID as JSON for AJAX requests
		if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
			respondJSON(w, map[string]string{"process_id": proc.ID})
			return
		}

		// Redirect to the processes page
		http.Redirect(w, r, "/processes", http.StatusSeeOther)
		return
	}

	// Get list of files for GET request
	files, err := h.getFiles(mangaPath)
	if err != nil {
		http.Redirect(w, r, "/manage-manga", http.StatusSeeOther)
		return
	}

	// Render template
	data := map[string]interface{}{
		"Title":      "Delete Files for " + mangaTitle,
		"MangaTitle": mangaTitle,
		"Files":      files,
		"Year":       time.Now().Year(),
	}

	renderTemplate(w, h.Templates, "delete_files.html", data, h.Logger)
}

// getFiles recursively gets all files in a directory
func (h *MangaOperationHandler) getFiles(dir string) ([]string, error) {
	var files []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			// Get relative path
			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}

			files = append(files, relPath)
		}

		return nil
	})

	return files, err
}

// StatusHandler handles status requests for processes
func (h *MangaOperationHandler) StatusHandler(w http.ResponseWriter, r *http.Request) {
	// Get process ID from query string
	processID := r.URL.Query().Get("id")
	if processID == "" {
		http.Error(w, "Process ID required", http.StatusBadRequest)
		return
	}

	// Get process status
	proc, exists := h.ProcessManager.GetProcess(processID)
	if !exists {
		http.Error(w, "Process not found", http.StatusNotFound)
		return
	}

	// Create the response with the specific format expected by the frontend
	response := struct {
		Status         string  `json:"status"`
		Progress       int     `json:"progress"`
		Total          int     `json:"total"`
		Message        string  `json:"message"`
		Error          string  `json:"error"`
		WaitingInput   bool    `json:"waiting_input"`
		InputPrompt    string  `json:"input_prompt"`
		InputType      string  `json:"input_type"`
		ProcessID      string  `json:"process_id"`
		SafeHTML       bool    `json:"safe_html"`
		ProcessType    string  `json:"process_type"`
		StartTime      string  `json:"start_time"`
		Duration       string  `json:"duration"`
		DownloadSpeed  float64 `json:"download_speed,omitempty"`
		DownloadedSize int64   `json:"downloaded_size,omitempty"`
		TotalSize      int64   `json:"total_size,omitempty"`
	}{
		Status:       string(proc.Status),
		Progress:     proc.Progress,
		Total:        proc.Total,
		Message:      proc.Message,
		Error:        proc.Error,
		WaitingInput: proc.IsWaiting,
		InputPrompt:  proc.InputPrompt,
		InputType:    proc.InputType,
		ProcessID:    proc.ID,
		SafeHTML:     strings.Contains(proc.InputPrompt, "<"),
		ProcessType:  string(proc.Type),
		StartTime:    proc.StartTime.Format(time.RFC3339),
		Duration:     time.Since(proc.StartTime).String(),
	}

	// Add download speed information if available
	if proc.Metadata != nil {
		if speed, ok := proc.Metadata["download_speed"].(float64); ok {
			response.DownloadSpeed = speed
		}
		if downloaded, ok := proc.Metadata["downloaded_size"].(int64); ok {
			response.DownloadedSize = downloaded
		}
		if totalSize, ok := proc.Metadata["total_size"].(int64); ok {
			response.TotalSize = totalSize
		}
	}

	// Return as JSON
	respondJSON(w, response)
}

// LogsHandler returns logs since a given sequence number
// DEPRECATED: Logs are now output to Docker stdout only
func (h *MangaOperationHandler) LogsHandler(w http.ResponseWriter, r *http.Request) {
	// Return empty logs array - logs are now in Docker container only
	respondJSON(w, map[string]interface{}{
		"logs": []map[string]interface{}{},
	})
}

// ProcessesAPIHandler returns processes as JSON for AJAX calls
func (h *MangaOperationHandler) ProcessesAPIHandler(w http.ResponseWriter, r *http.Request) {
	// Get all processes
	allProcesses := h.ProcessManager.ListProcesses()

	// Format processes for frontend
	type ProcessResponse struct {
		ID                 string    `json:"id"`
		Type               string    `json:"type"`
		Title              string    `json:"title"`
		Status             string    `json:"status"`
		Progress           int       `json:"progress"`
		Total              int       `json:"total"`
		ProgressPercentage int       `json:"progress_percentage"`
		Message            string    `json:"message"`
		Error              string    `json:"error"`
		StartTime          time.Time `json:"start_time"`
		EndTime            time.Time `json:"end_time"`
		IsWaiting          bool      `json:"is_waiting"`
		InputPrompt        string    `json:"input_prompt,omitempty"`
		InputType          string    `json:"input_type,omitempty"`
	}

	responseProcesses := make([]ProcessResponse, 0, len(allProcesses))
	for _, proc := range allProcesses {
		// Calculate percentage
		percentage := 0
		if proc.Total > 0 {
			percentage = (proc.Progress * 100) / proc.Total
		}

		responseProcesses = append(responseProcesses, ProcessResponse{
			ID:                 proc.ID,
			Type:               string(proc.Type),
			Title:              proc.Title,
			Status:             string(proc.Status),
			Progress:           proc.Progress,
			Total:              proc.Total,
			ProgressPercentage: percentage,
			Message:            proc.Message,
			Error:              proc.Error,
			StartTime:          proc.StartTime,
			EndTime:            proc.EndTime,
			IsWaiting:          proc.IsWaiting,
			InputPrompt:        proc.InputPrompt,
			InputType:          proc.InputType,
		})
	}

	// Return as JSON
	respondJSON(w, map[string]interface{}{
		"processes": responseProcesses,
	})
}

// ProcessCancelHandler cancels a process
func (h *MangaOperationHandler) ProcessCancelHandler(w http.ResponseWriter, r *http.Request) {
	// Get process ID from URL
	vars := mux.Vars(r)
	processID := vars["id"]

	// Check if this is a force cancel
	forceCancel := r.URL.Query().Get("force") == "true"

	// Cancel or force cancel the process using ProcessManager
	var success bool
	if forceCancel {
		success = h.ProcessManager.ForceCancelProcess(processID)
		if success {
			h.Logger("INFO", fmt.Sprintf("Process %s force cancelled", processID))
		}
	} else {
		success = h.ProcessManager.CancelProcess(processID)
		if success {
			h.Logger("INFO", fmt.Sprintf("Process %s cancelled", processID))
		}
	}

	if !success {
		http.Error(w, "Process not found or not running", http.StatusNotFound)
		return
	}

	// Return JSON response for AJAX requests
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		respondJSONSuccess(w, nil)
		return
	}

	// Redirect back to processes page for regular requests
	http.Redirect(w, r, "/processes", http.StatusSeeOther)
}

// ProcessRerunHandler reruns a process with the same parameters
func (h *MangaOperationHandler) ProcessRerunHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow POST requests
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get process ID from URL
	vars := mux.Vars(r)
	processID := vars["id"]

	// Get the original process
	originalProc, exists := h.ProcessManager.GetProcess(processID)
	if !exists {
		h.Logger("ERROR", fmt.Sprintf("Process not found for rerun: %s", processID))
		respondJSONError(w, http.StatusNotFound, "Process not found")
		return
	}

	// Create a new process with the same parameters
	newProc := h.ProcessManager.NewProcess(originalProc.Type, originalProc.Title)
	h.Logger("INFO", fmt.Sprintf("Rerunning process %s as new process %s", processID, newProc.ID))

	// Execute the operation based on process type
	switch originalProc.Type {
	case internal.ProcessTypeMetadataUpdate:
		// Get URLs from original process metadata
		mangareaderURL := ""
		mangadexURL := ""
		if val, ok := originalProc.Metadata["mangareader_url"].(string); ok {
			mangareaderURL = val
		}
		if val, ok := originalProc.Metadata["mangadex_url"].(string); ok {
			mangadexURL = val
		}

		// Create cancel channels
		cancelChan, forceCancelChan := setupProcessCancellation(newProc)

		// Initialize the prompt manager
		initializePromptManager := createPromptManagerInitializer(h.Logger)

		// Get safe WebInput function
		webInputFunc := getWebInputFunc(h.WebInput, h.Logger)

		// Prepare data for ProcessManga
		threadData := map[string]interface{}{
			"manga_title":      originalProc.Title,
			"mangareader_url":  mangareaderURL,
			"mangadex_url":     mangadexURL,
			"download_url":     "",
			"is_manga":         true,
			"is_oneshot":       false,
			"delete_originals": false,
			"language":         "en",
			"update_metadata":  true,
		}

		// Start metadata update using ProcessManga
		go processors.ProcessManga(
			threadData,
			cancelChan,
			forceCancelChan,
			convertToProcessorsAppConfig(h.Config, newProc.ID, h.Logger),
			h.ProcessManager,
			newProc.ID,
			webInputFunc,
			h.Logger,
			initializePromptManager,
		)
	case internal.ProcessTypeProcess:
		// Start process in a goroutine - now fully implemented
		go h.executeProcessOperation(newProc, originalProc.Title)
	case internal.ProcessTypeDelete:
		// Start deletion in a goroutine
		go h.executeDeleteProcess(newProc, originalProc.Title)
	default:
		h.Logger("WARNING", fmt.Sprintf("Process type %s not supported for rerun", originalProc.Type))
	}

	// Return success response
	respondJSONSuccess(w, map[string]interface{}{
		"processId":   newProc.ID,
		"originalId":  processID,
		"processType": string(newProc.Type),
	})
}

// ProcessDeleteHandler deletes a process from history
func (h *MangaOperationHandler) ProcessDeleteHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow POST requests
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get process ID from URL
	vars := mux.Vars(r)
	processID := vars["id"]

	// Get the process to check if it exists and is not running
	proc, exists := h.ProcessManager.GetProcess(processID)
	if !exists {
		h.Logger("ERROR", fmt.Sprintf("Process not found for deletion: %s", processID))
		respondJSONError(w, http.StatusNotFound, "Process not found")
		return
	}

	// Don't allow deleting running processes
	if proc.Status == internal.ProcessStatusRunning {
		h.Logger("ERROR", fmt.Sprintf("Cannot delete running process: %s", processID))
		respondJSONError(w, http.StatusBadRequest, "Cannot delete a running process")
		return
	}

	// Delete the process
	success := h.ProcessManager.DeleteProcess(processID)
	if !success {
		h.Logger("ERROR", fmt.Sprintf("Failed to delete process: %s", processID))
		respondJSONError(w, http.StatusInternalServerError, "Failed to delete process")
		return
	}

	h.Logger("INFO", fmt.Sprintf("Process %s deleted from history", processID))

	// Return success response
	respondJSONSuccess(w, nil)
}

// executeDeleteProcess handles the execution of a delete process
func (h *MangaOperationHandler) executeDeleteProcess(proc *internal.Process, mangaTitle string) {
	// Set up process cancellation
	setupProcessCancellation(proc)

	// Process cleanup when finished
	defer func() {
		if r := recover(); r != nil {
			h.Logger("ERROR", fmt.Sprintf("Panic in manga deletion: %v", r))
			h.ProcessManager.FailProcess(proc.ID, fmt.Sprintf("Panic: %v", r))
		}
	}()

	// Get path
	mangaPath := filepath.Join(h.Config.MangaBaseDir, mangaTitle)

	h.Logger("INFO", fmt.Sprintf("Deleting directory: %s", mangaPath))
	proc.Update(0, 1, "Deleting manga directory")

	// Delete the directory using simple file operations
	if err := os.RemoveAll(mangaPath); err != nil {
		if os.IsNotExist(err) {
			h.Logger("INFO", fmt.Sprintf("Directory %s already deleted", mangaPath))
		} else {
			h.Logger("ERROR", fmt.Sprintf("Error deleting directory: %v", err))
			h.ProcessManager.FailProcess(proc.ID, fmt.Sprintf("Error deleting directory: %v", err))
			return
		}
	}

	h.Logger("INFO", fmt.Sprintf("Successfully deleted directory: %s", mangaPath))
	proc.Update(1, 1, "Deletion complete")
	h.ProcessManager.CompleteProcess(proc.ID)
}

// executeProcessOperation handles the execution of a manga process operation
func (h *MangaOperationHandler) executeProcessOperation(proc *internal.Process, mangaTitle string) {
	// Set up process cancellation
	cancelChan, forceCancelChan := setupProcessCancellation(proc)

	// Process cleanup when finished
	defer func() {
		if r := recover(); r != nil {
			h.Logger("ERROR", fmt.Sprintf("Panic in process operation: %v", r))
			h.ProcessManager.FailProcess(proc.ID, fmt.Sprintf("Panic: %v", r))
		}
	}()

	// Check for cancellation before starting
	select {
	case <-cancelChan:
		h.ProcessManager.CancelProcess(proc.ID)
		return
	case <-forceCancelChan:
		h.ProcessManager.ForceCancelProcess(proc.ID)
		return
	default:
	}

	// Get cached sources for this manga
	cachedSources, err := cache.GetCachedSources(mangaTitle)
	if err != nil {
		h.Logger("WARNING", fmt.Sprintf("Could not retrieve cached sources for %s: %v", mangaTitle, err))
		// Continue with empty sources
		cachedSources = cache.MangaSource{}
	}

	// Check if we have enough information to restart this process
	needsManualRestart := false

	// If we have cached sources, this process can be restarted
	if cachedSources.MangaReader != "" || cachedSources.MangaDex != "" || cachedSources.DownloadURL != "" {
		// For ADDED processes we need to create a special web handler endpoint to restart the process
		h.Logger("INFO", fmt.Sprintf("Process for %s needs manual restart using cached parameters (Reader: %s, DexURL: %s, DL: %s)",
			mangaTitle, cachedSources.MangaReader, cachedSources.MangaDex, cachedSources.DownloadURL))

		// Create redirect link for manual restart
		restartURL := fmt.Sprintf("/process-rerun/%s", proc.ID)

		// Update the process with instructions on how to properly restart it
		proc.Update(100, 100, fmt.Sprintf("To complete this process, go to: %s", restartURL))
		h.Logger("INFO", fmt.Sprintf("Process %s created, ready for restart at %s", proc.ID, restartURL))

		needsManualRestart = true
	} else {
		h.Logger("WARNING", fmt.Sprintf("No cached sources found for %s, can't create restart URL", mangaTitle))
		proc.Update(100, 100, "No source information available for restart")
	}

	// Mark the process appropriately based on whether it needs manual restart
	if needsManualRestart {
		// Set process as waiting for user input (which it is - waiting for the user to restart it)
		proc.IsWaiting = true

		// Save the update to the process using the correct method signature
		h.ProcessManager.UpdateProcess(proc.ID, func(p *internal.Process) {
			p.IsWaiting = true
			// Keep the current message about manual restart
		})
	} else {
		// Complete the process normally
		h.ProcessManager.CompleteProcess(proc.ID)
	}
}

// ProcessRerunStartHandler starts a process that was re-created
func (h *MangaOperationHandler) ProcessRerunStartHandler(w http.ResponseWriter, r *http.Request) {
	// Get process ID from URL
	vars := mux.Vars(r)
	processID := vars["id"]

	// Get the process
	proc, exists := h.ProcessManager.GetProcess(processID)
	if !exists {
		http.Redirect(w, r, "/processes", http.StatusSeeOther)
		return
	}

	// Make sure it's a 'process' type
	if proc.Type != internal.ProcessTypeProcess {
		http.Redirect(w, r, "/processes", http.StatusSeeOther)
		return
	}

	// Get cached sources for this manga title
	cachedSources, err := cache.GetCachedSources(proc.Title)
	if err != nil {
		h.Logger("WARNING", fmt.Sprintf("Could not retrieve cached sources for %s: %v", proc.Title, err))
		// Continue anyway with empty values
	}

	// Create form data from cached sources and process info
	formData := ProcessFormData{
		MangaTitle:       proc.Title,
		MangaReaderURL:   cachedSources.MangaReader,
		MangaDexURL:      cachedSources.MangaDex,
		DownloadURL:      cachedSources.DownloadURL,
		IsOneshot:        cachedSources.IsOneshot,
		DeleteOriginals:  false,
		Language:         "en",
		IsUpdateMetadata: false,
	}

	// Mark original process as complete
	h.ProcessManager.CompleteProcess(processID)

	// Create a new pending process to store form data
	// This replaces the session-based state management
	newProc := h.ProcessManager.NewProcess(internal.ProcessTypeProcess, proc.Title)
	newProc.Metadata = map[string]interface{}{
		"form_data": formData,
		"pending":   true, // Mark as pending until ProcessHandler starts it
	}
	h.Logger("INFO", fmt.Sprintf("Created new process %s to rerun %s", newProc.ID, proc.Title))

	// Redirect to process page with the new process ID
	http.Redirect(w, r, fmt.Sprintf("/process?id=%s", newProc.ID), http.StatusSeeOther)
}
