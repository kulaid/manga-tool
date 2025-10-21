package processors

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"manga-tool/internal"
	"manga-tool/internal/cache"
	"manga-tool/internal/komga"
	"manga-tool/internal/processor"
	"manga-tool/internal/scraper"
	"manga-tool/internal/util"
)

// AppConfig holds configuration needed by the processManga function
type AppConfig struct {
	MangaBaseDir     string
	TempDir          string
	PromptTimeout    time.Duration
	RealDebridAPIKey string
	MadokamiUsername string
	MadokamiPassword string
	Komga            komga.Config
	Parallelism      int
}

// Process manga function - this is where the manga processor would be called
func ProcessManga(threadData map[string]interface{}, cancelChan chan struct{}, forceCancelChan chan struct{},
	appConfig AppConfig, processManager *internal.ProcessManager, currentProcessID string,
	webInput func(processID, prompt, inputType string) string, logFunc func(level, message string),
	initializePromptManager func(process *internal.Process)) {

	mangaTitle, _ := threadData["manga_title"].(string)
	downloadURL, _ := threadData["download_url"].(string)
	downloadUsername, _ := threadData["download_username"].(string)
	downloadPassword, _ := threadData["download_password"].(string)
	isOneshot, _ := threadData["is_oneshot"].(bool)
	mangareaderURL, _ := threadData["mangareader_url"].(string)
	mangadexURL, _ := threadData["mangadex_url"].(string)
	// isManga is not used directly but keeping for completeness
	_ = threadData["is_manga"].(bool)
	deleteOriginals, _ := threadData["delete_originals"].(bool)
	language, _ := threadData["language"].(string)
	updateMetadata, _ := threadData["update_metadata"].(bool) // Check if this is a metadata update

	// Get the current process
	proc, exists := processManager.GetProcess(currentProcessID)
	if !exists {
		logFunc("ERROR", "No active process found for processing manga")
		return
	}

	// Create sync.Once instances to ensure channels are only closed once
	var cancelOnce sync.Once
	var forceCancelOnce sync.Once

	// Set the cancel functions with safe channel closing
	proc.CancelFunc = func() {
		cancelOnce.Do(func() {
			select {
			case <-cancelChan:
				// Already closed
			default:
				close(cancelChan)
			}
		})
	}
	proc.ForceCancel = func() {
		forceCancelOnce.Do(func() {
			select {
			case <-forceCancelChan:
				// Already closed
			default:
				close(forceCancelChan)
			}
		})
	}

	// Initialize the prompt manager with the current process
	initializePromptManager(proc)
	logFunc("INFO", "Initialized prompt manager for process ID: "+proc.ID)

	// Check for cancellation at the start
	select {
	case <-cancelChan:
		processManager.CancelProcess(proc.ID)
		return
	case <-forceCancelChan:
		processManager.ForceCancelProcess(proc.ID)
		return
	default:
	}

	// Reset processing status for this manga
	proc.Update(0, 100, fmt.Sprintf("Starting processing for %s", mangaTitle))

	// Initialize logger for this process
	logger := util.NewSimpleLogger(currentProcessID, logFunc)

	// Record the start time but not using it directly
	_ = time.Now()

	// Declare mangaTempDir at function scope so it can be shared between sections
	var mangaTempDir string
	var mangaTargetDir string
	var cleanupTempDir bool           // Track whether we should cleanup temp dir
	var metadataUpdateInProgress bool // Track if we're in metadata update mode

	// Defer cleanup function to handle cancellation during metadata update
	defer func() {
		// If cancelled/failed during metadata update, restore files from temp back to Komga directory
		if metadataUpdateInProgress && cleanupTempDir && mangaTempDir != "" && mangaTargetDir != "" {
			// Check if process was cancelled or failed (failed could be due to cancellation)
			if proc.Status == internal.ProcessStatusCancelled || proc.Status == internal.ProcessStatusFailed {
				logger.Warning("Process cancelled/failed during metadata update - restoring original files...")

				// Find files in temp directory
				tempFiles, err := util.FindCBZFilesFromMount(mangaTempDir)
				if err == nil && len(tempFiles) > 0 {
					logger.Info(fmt.Sprintf("Restoring %d files from temp to %s", len(tempFiles), mangaTargetDir))

					// Move files back to original location
					restoredCount := 0
					for _, tempFile := range tempFiles {
						originalPath := filepath.Join(mangaTargetDir, filepath.Base(tempFile))
						if err := util.CopyFile(tempFile, originalPath); err != nil {
							logger.Error(fmt.Sprintf("Failed to restore file %s: %v", filepath.Base(tempFile), err))
						} else {
							restoredCount++
							logger.Info(fmt.Sprintf("Restored: %s", filepath.Base(tempFile)))
						}
					}
					logger.Info(fmt.Sprintf("File restoration complete - %d/%d files restored to %s", restoredCount, len(tempFiles), mangaTargetDir))
				} else {
					logger.Warning("No files found in temp directory to restore")
				}

				// Clean up temp directory
				logger.Info(fmt.Sprintf("Removing temp directory: %s", mangaTempDir))
				os.RemoveAll(mangaTempDir)
			}
		}
	}() // Handle metadata update mode - copy files to temp and reprocess from step 3
	if updateMetadata {
		metadataUpdateInProgress = true
		proc.Update(5, 100, "Metadata update mode: Copying files to temp directory...")
		logger.Info(fmt.Sprintf("Starting metadata update for %s", mangaTitle))

		// Check for cancellation
		select {
		case <-cancelChan:
			processManager.CancelProcess(proc.ID)
			return
		case <-forceCancelChan:
			processManager.ForceCancelProcess(proc.ID)
			return
		default:
		}

		// Find existing CBZ files in the target directory
		mangaTargetDir = filepath.Join(appConfig.MangaBaseDir, mangaTitle)
		existingFiles, err := util.FindCBZFilesFromMount(mangaTargetDir)
		if err != nil {
			logger.Error(fmt.Sprintf("Error finding CBZ files: %v", err))
			processManager.FailProcess(proc.ID, fmt.Sprintf("Error finding CBZ files: %v", err))
			return
		}

		if len(existingFiles) == 0 {
			logger.Error("No CBZ files found for this manga")
			processManager.FailProcess(proc.ID, "No CBZ files found for this manga")
			return
		}

		logger.Info(fmt.Sprintf("Found %d CBZ files to reprocess", len(existingFiles)))

		// Create a manga-specific temp directory for reprocessing
		mangaTempDir = filepath.Join(appConfig.TempDir, fmt.Sprintf("manga_%s_%s", mangaTitle, proc.ID))
		if err := os.MkdirAll(mangaTempDir, 0755); err != nil {
			logger.Error(fmt.Sprintf("Failed to create temp directory: %v", err))
			processManager.FailProcess(proc.ID, fmt.Sprintf("Failed to create temp directory: %v", err))
			return
		}
		// DO NOT defer cleanup here - we'll clean up at the end after processing
		cleanupTempDir = true // Mark that we created this temp dir and should clean it up

		// MOVE files from Komga directory to temp directory in parallel
		// Since they're on different filesystems, we copy then delete
		proc.Update(10, 100, "Moving files to temp directory...")
		logger.Info(fmt.Sprintf("Moving %d files from %s to %s", len(existingFiles), mangaTargetDir, mangaTempDir))

		// Move ALL files concurrently without limiting parallelism
		var wg sync.WaitGroup
		var mu sync.Mutex
		movedCount := 0
		startTime := time.Now()

		logger.Info(fmt.Sprintf("Starting concurrent move of all %d files...", len(existingFiles)))

		for _, srcFile := range existingFiles {
			wg.Add(1)

			go func(src string) {
				defer wg.Done()

				dstFile := filepath.Join(mangaTempDir, filepath.Base(src))

				// Copy the file
				if err := util.CopyFile(src, dstFile); err != nil {
					logger.Warning(fmt.Sprintf("Failed to copy file %s: %v", filepath.Base(src), err))
					return
				}

				// Delete the original file after successful copy
				if err := os.Remove(src); err != nil {
					logger.Warning(fmt.Sprintf("Failed to remove original file %s: %v", filepath.Base(src), err))
					// Continue anyway - the copy succeeded
				}

				mu.Lock()
				movedCount++
				mu.Unlock()
			}(srcFile)
		}

		// Wait for all moves to complete
		wg.Wait()
		elapsed := time.Since(startTime)

		logger.Info(fmt.Sprintf("Successfully moved %d/%d files in %v (all concurrent)", movedCount, len(existingFiles), elapsed.Round(time.Millisecond)))

		// Don't delete temp files during processing - we need them to reprocess
		deleteOriginals = false

		// Files moved to temp. Continue with normal flow from step 3 (Find CBZ files)
		// After processing, new files with new names will be in the target directory
		logger.Info("Files moved to temp directory. Continuing with normal processing flow...")
	}

	// Regular manga processing
	proc.Update(0, 100, "Starting manga processing...")
	logger.Info(fmt.Sprintf("Starting manga processing for %s", mangaTitle))

	// Create a manga-specific temp directory (if not already created by metadata update mode)
	if mangaTempDir == "" {
		mangaTempDir = filepath.Join(appConfig.TempDir, fmt.Sprintf("manga_%s_%s", mangaTitle, proc.ID))
	}

	tempDirAlreadyExists := false
	if _, err := os.Stat(mangaTempDir); err == nil {
		// Directory already exists (from metadata update mode)
		tempDirAlreadyExists = true
		logger.Info(fmt.Sprintf("Using existing temp directory: %s", mangaTempDir))
	} else {
		// Create new temp directory
		if err := os.MkdirAll(mangaTempDir, 0755); err != nil {
			logger.Error(fmt.Sprintf("Failed to create manga temp directory: %v", err))
			processManager.FailProcess(proc.ID, fmt.Sprintf("Failed to create manga temp directory: %v", err))
			return
		}
		cleanupTempDir = true // Mark for cleanup
	}

	// Copy uploaded files from upload directory to processing directory (skip if from metadata update)
	if !tempDirAlreadyExists {
		uploadDir := filepath.Join(appConfig.TempDir, "manga_uploads")
		if _, err := os.Stat(uploadDir); err == nil {
			// Upload directory exists, copy all files to processing directory
			proc.Update(5, 100, "Copying uploaded files...")
			logger.Info(fmt.Sprintf("Copying uploaded files from %s to %s", uploadDir, mangaTempDir))

			files, err := os.ReadDir(uploadDir)
			if err != nil {
				logger.Error(fmt.Sprintf("Failed to read upload directory: %v", err))
			} else {
				for _, file := range files {
					if !file.IsDir() {
						srcPath := filepath.Join(uploadDir, file.Name())
						dstPath := filepath.Join(mangaTempDir, file.Name())

						if err := util.CopyFile(srcPath, dstPath); err != nil {
							logger.Warning(fmt.Sprintf("Failed to copy uploaded file %s: %v", file.Name(), err))
						} else {
							logger.Info(fmt.Sprintf("Copied uploaded file: %s", file.Name()))
						}
					}
				}
			}
		}
	}

	// Download manga (skip if from metadata update)
	if downloadURL != "" && !tempDirAlreadyExists {
		proc.Update(10, 100, fmt.Sprintf("Downloading manga from %s...", downloadURL))
		logger.Info(fmt.Sprintf("DOWNLOAD STARTING: Fetching manga from %s", downloadURL))

		if err := util.DownloadFile(downloadURL, mangaTempDir, downloadUsername, downloadPassword, appConfig.RealDebridAPIKey, appConfig.MadokamiUsername, appConfig.MadokamiPassword, logger); err != nil {
			logger.Error(fmt.Sprintf("Download failed: %v", err))
			processManager.FailProcess(proc.ID, fmt.Sprintf("Download failed: %v", err))
			return
		}
		logger.Info("Download completed successfully")
	}

	// Check for RAR files and convert them (skip if from metadata update - already CBZ files)
	if !tempDirAlreadyExists {
		proc.Update(20, 100, "Checking for RAR files...")
		logger.Info(fmt.Sprintf("Starting RAR extraction in: %s", mangaTempDir))

		// Create extraction logger
		extractionLogger := util.NewSimpleLogger(currentProcessID, logFunc)

		progressCallback := func(progressMsg string) {
			proc.Update(25, 100, progressMsg)
			logger.Info(progressMsg)
		}

		if err := util.SuperSafeExtractRars(mangaTempDir, extractionLogger, progressCallback, appConfig.Parallelism); err != nil {
			logger.Warning(fmt.Sprintf("RAR extraction encountered issues: %v", err))
			// Continue anyway - we'll work with whatever files were successfully extracted
		}
	}

	// Find CBZ files (STEP 3 - this is where metadata reprocess starts)
	proc.Update(30, 100, "Finding CBZ files...")
	logger.Info(fmt.Sprintf("Starting CBZ file search in directory: %s", mangaTempDir))
	logger.Info(fmt.Sprintf("Directory exists: %v", util.DirExists(mangaTempDir)))
	logger.Info(fmt.Sprintf("Directory permissions: %v", util.GetDirPermissions(mangaTempDir)))

	cbzFiles, err := util.FindCBZFiles(mangaTempDir)
	if err != nil {
		logger.Error(fmt.Sprintf("Error finding files: %v", err))
		processManager.FailProcess(proc.ID, fmt.Sprintf("Error finding files: %v", err))
		return
	}

	if len(cbzFiles) == 0 {
		logger.Error("No CBZ files found in the upload directory")
		processManager.FailProcess(proc.ID, "No CBZ files found in the upload directory")
		return
	}

	logger.Info(fmt.Sprintf("Found %d CBZ files to process", len(cbzFiles)))

	// Ask user which files to delete before processing
	proc.Update(35, 100, "Select files to delete (if needed)...")
	logger.Info("Checking if any files should be deleted before processing...")

	if err := util.AskToDeleteFilesWithWebInput(mangaTempDir, logger, func(prompt, inputType string) string {
		return webInput(currentProcessID, prompt, inputType)
	}); err != nil {
		logger.Warning(fmt.Sprintf("Error during file deletion selection: %v", err))
	}

	// Analyze the files to determine which chapters need titles
	proc.Update(40, 100, "Analyzing files for chapter information...")
	logger.Info("Analyzing files for chapter information...")

	var neededChapters map[float64]bool
	var discoveredTitles map[float64]string

	if isOneshot {
		// For oneshots, we don't need to analyze - just use chapter 1
		neededChapters = make(map[float64]bool)
		discoveredTitles = make(map[float64]string)
		logger.Info("Oneshot mode: Skipping file analysis, will use chapter 1")
	} else {
		neededChapters, discoveredTitles = processor.AnalyzeChaptersNeeded(cbzFiles, logger)
		logger.Info(fmt.Sprintf("Found %d chapters that might need titles", len(neededChapters)))
	}

	// Initialize chapter titles map
	chapterTitles := make(map[float64]string)

	// Try to load cached chapter titles first
	cached, err := cache.GetCachedChapterTitles(mangaTitle)
	if err == nil && len(cached) > 0 {
		logger.Info(fmt.Sprintf("Loaded %d cached chapter titles", len(cached)))
		for ch, title := range cached {
			chapterTitles[ch] = title
			logger.Info(fmt.Sprintf("Using cached title: Chapter %g: %s", ch, title))
		}
	} else {
		logger.Info("No cached chapter titles found")
	}

	// Add in any titles discovered from directory structure (these override cache)
	for chNum, title := range discoveredTitles {
		chapterTitles[chNum] = title
		logger.Info(fmt.Sprintf("Using title from directory structure: Chapter %g: %s", chNum, title))
	}

	// Try to get titles from MangaReader and MangaDex if URLs are provided
	if mangareaderURL != "" {
		proc.Update(45, 100, "Fetching chapter titles from MangaReader.to...")
		logger.Info(fmt.Sprintf("Trying to fetch chapter titles from MangaReader.to: %s", mangareaderURL))

		mangaReaderTitles := scraper.GetMangaReaderChapters(mangareaderURL, logger, func(prompt, inputType string) string {
			return webInput(currentProcessID, prompt, inputType)
		})
		logger.Info(fmt.Sprintf("Found %d chapter titles from MangaReader.to", len(mangaReaderTitles)))

		// Add titles to our collection
		for ch, title := range mangaReaderTitles {
			chapterTitles[ch] = title
		}
	}

	if mangadexURL != "" {
		proc.Update(50, 100, "Fetching chapter titles from MangaDex...")
		logger.Info(fmt.Sprintf("Trying to fetch chapter titles from MangaDex: %s", mangadexURL))

		mangaDexTitles := scraper.GetMangaDexChapters(mangadexURL, logger)
		logger.Info(fmt.Sprintf("Found %d chapter titles from MangaDex", len(mangaDexTitles)))

		// Add titles to our collection, but don't overwrite MangaReader titles
		for ch, title := range mangaDexTitles {
			if _, exists := chapterTitles[ch]; !exists {
				chapterTitles[ch] = title
			}
		}
	}

	// Handle oneshot mode - override chapter titles to set chapter 1 as the manga title
	if isOneshot {
		chapterTitles = map[float64]string{1.0: mangaTitle}
		logger.Info(fmt.Sprintf("Oneshot mode: Setting chapter 1 title to '%s'", mangaTitle))
	}

	// Collect missing chapter titles from user input if needed
	proc.Update(55, 100, "Collecting missing chapter titles...")
	logger.Info("Checking for missing chapter titles...")

	// Skip missing chapter collection for oneshots since we've already set the title
	if !isOneshot && len(neededChapters) > 0 {
		missingTitles := make(map[float64]bool)
		for ch := range neededChapters {
			if _, exists := chapterTitles[ch]; !exists {
				missingTitles[ch] = true
			}
		}

		if len(missingTitles) > 0 {
			logger.Info(fmt.Sprintf("Need to collect titles for %d chapters", len(missingTitles)))
			chapterTitles = CollectMissingChapterTitles(missingTitles, chapterTitles, logger, func(prompt, inputType string) string {
				return webInput(currentProcessID, prompt, inputType)
			})
			logger.Info(fmt.Sprintf("Chapter title collection complete. Total titles: %d", len(chapterTitles)))
		}
	} else if isOneshot {
		logger.Info("Skipping missing chapter collection for oneshot")
	}

	// Save chapter titles to cache
	cache.SaveChapterTitles(mangaTitle, chapterTitles)

	// Process the files with the processor
	proc.Update(60, 100, "Processing manga files...")
	logger.Info("Starting manga processor...")

	// Debug information
	logger.Info(fmt.Sprintf("Number of files to process: %d", len(cbzFiles)))
	for i, file := range cbzFiles {
		logger.Info(fmt.Sprintf("File %d: %s", i+1, file))
	}

	// Process the files
	mangaTargetDir = filepath.Join(appConfig.MangaBaseDir, mangaTitle)
	err = processManga(cbzFiles, mangaTargetDir, mangaTitle, chapterTitles, logger, proc, cancelChan, deleteOriginals, language, isOneshot, appConfig.Parallelism, func(prompt, inputType string) string {
		return webInput(currentProcessID, prompt, inputType)
	})
	if err != nil {
		logger.Error(fmt.Sprintf("Error processing manga: %v", err))
		processManager.FailProcess(proc.ID, fmt.Sprintf("Error processing manga: %v", err))
		return
	}

	// Save all source URLs to cache with oneshot flag
	// Only update the URLs that were actually provided in this process
	if err := cache.SaveSources(mangaTitle, mangareaderURL, mangadexURL, downloadURL, isOneshot); err != nil {
		logger.Warning(fmt.Sprintf("Failed to save source URLs to cache: %v", err))
	}

	// Mark metadata update as complete (so defer won't try to restore files)
	metadataUpdateInProgress = false

	// Clean up
	proc.Update(90, 100, "Cleaning up temporary files...")
	logger.Info("Cleaning up temporary files...")

	// Clean up the temporary directory only if we created it
	if cleanupTempDir {
		logger.Info(fmt.Sprintf("Removing temp directory: %s", mangaTempDir))
		os.RemoveAll(mangaTempDir)
	}

	// Refresh Komga library
	proc.Update(95, 100, "Refreshing Komga libraries...")
	logger.Info("Refreshing Komga libraries...")

	// Get Komga configuration directly from environment variables
	komgaURL := os.Getenv("KOMGA_URL")
	if komgaURL == "" {
		komgaURL = appConfig.Komga.URL // Fallback to config if env var not set
	}

	komgaUsername := os.Getenv("KOMGA_USERNAME")
	if komgaUsername == "" {
		komgaUsername = appConfig.Komga.Username
	}

	komgaPassword := os.Getenv("KOMGA_PASSWORD")
	if komgaPassword == "" {
		komgaPassword = appConfig.Komga.Password
	}

	komgaRefreshEnabled := true // Always enable refresh for this operation

	// Get libraries from environment variable
	// If KOMGA_LIBRARIES is set (even if empty), use it - empty means refresh ALL libraries
	// If KOMGA_LIBRARIES is not set, fall back to config
	var libraries []string
	komgaLibraries, komgaLibrariesSet := os.LookupEnv("KOMGA_LIBRARIES")
	if komgaLibrariesSet {
		// Environment variable is set - use it (empty = refresh all)
		if komgaLibraries != "" {
			libraries = strings.Split(komgaLibraries, ",")
			// Filter out empty entries
			filteredLibraries := make([]string, 0)
			for _, lib := range libraries {
				if strings.TrimSpace(lib) != "" {
					filteredLibraries = append(filteredLibraries, lib)
				}
			}
			libraries = filteredLibraries
		}
		// If komgaLibraries is "", libraries stays as empty slice = refresh all
	} else {
		// Environment variable not set - use config
		if len(appConfig.Komga.Libraries) > 0 {
			libraries = appConfig.Komga.Libraries
		}
	}

	// Log the configuration we're using
	logger.Info(fmt.Sprintf("Komga refresh configuration: URL=%s, Username=%s, Libraries=%v",
		komgaURL, komgaUsername, libraries))

	// Create client with explicit configuration
	komgaClient := komga.NewClient(&komga.Config{
		URL:            komgaURL,
		Username:       komgaUsername,
		Password:       komgaPassword,
		Libraries:      libraries,
		RefreshEnabled: komgaRefreshEnabled,
		Logger:         logger,
	})

	if komgaClient.RefreshAllLibraries() {
		logger.Info("Komga libraries refreshed successfully")
	} else {
		logger.Warning("Failed to refresh Komga libraries")
	}

	// Mark process as complete
	proc.Update(100, 100, "Processing complete!")
	logger.Info(fmt.Sprintf("Processing complete for %s", mangaTitle))
	processManager.CompleteProcess(proc.ID)
}

// CollectMissingChapterTitles collects missing chapter titles through user input
func CollectMissingChapterTitles(neededChapters map[float64]bool, existingTitles map[float64]string, logger util.Logger, webInput func(prompt string, inputType string) string) map[float64]string {
	result := make(map[float64]string)

	// Copy existing titles to result
	for ch, title := range existingTitles {
		result[ch] = title
	}

	// Sort chapter numbers for consistent order
	chapterNumbers := make([]float64, 0, len(neededChapters))
	for ch := range neededChapters {
		chapterNumbers = append(chapterNumbers, ch)
	}

	// Sort them
	sort.Float64s(chapterNumbers)

	// Ask for titles for each chapter
	for _, ch := range chapterNumbers {
		// Skip if we already have a title
		if _, exists := result[ch]; exists {
			continue
		}

		// Create a prompt for this chapter
		promptStr := fmt.Sprintf("Enter title for Chapter %.1f", ch)

		// Modify the prompt if it's a whole number
		if ch == float64(int(ch)) {
			promptStr = fmt.Sprintf("Enter title for Chapter %d", int(ch))
		}

		// Get the title from web input
		title := webInput(promptStr, "text")

		// If we got a title, add it
		if title != "" {
			result[ch] = title
			logger.Info(fmt.Sprintf("Added title for Chapter %.1f: %s", ch, title))
		}
	}

	return result
}

// processManga is a replacement for processor.ProcessManga
func processManga(files []string, targetDir string, mangaTitle string, chapterTitles map[float64]string,
	logger util.Logger, proc *internal.Process, cancelChan chan struct{}, deleteOriginals bool, language string, isOneshot bool, parallelism int,
	webInput func(prompt, inputType string) string) error {

	// Check for cancellation before starting
	select {
	case <-cancelChan:
		logger.Info("Processing cancelled before starting")
		return fmt.Errorf("processing cancelled by user")
	default:
	}

	// Create target directory if it doesn't exist
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	// Configure the processor with the collected titles
	config := &processor.Config{
		Process:         proc,
		Logger:          logger,
		ChapterTitles:   chapterTitles,
		IsManga:         true,
		IsOneshot:       isOneshot,
		DeleteOriginals: deleteOriginals,
		Language:        language,
		Parallelism:     parallelism, // Use passed parallelism value
		// Add function to prompt for chapter titles within volumes
		GetChapterFunc: func(chapterNum float64) string {
			// Check for cancellation before prompting user
			select {
			case <-cancelChan:
				logger.Info("Processing cancelled during chapter title prompt")
				return ""
			default:
			}

			// Format chapter number display
			var promptStr string
			if chapterNum == float64(int(chapterNum)) {
				promptStr = fmt.Sprintf("Enter title for Chapter %d", int(chapterNum))
			} else {
				promptStr = fmt.Sprintf("Enter title for Chapter %.1f", chapterNum)
			}

			// Check if we already have a title
			if title, exists := chapterTitles[chapterNum]; exists {
				return title
			}

			// Get title from user
			title := webInput(promptStr, "text")
			if title != "" {
				// Save to our local map for future reference
				chapterTitles[chapterNum] = title
				logger.Info(fmt.Sprintf("Added title for Chapter %.1f: %s", chapterNum, title))
			}
			return title
		},
	}

	// Create a done channel to signal when processing is complete
	done := make(chan error, 1)

	// Start processing in a goroutine so we can monitor for cancellation
	go func() {
		// Process files in parallel, directly to the target directory
		done <- processor.ProcessBatch(files, mangaTitle, targetDir, config)
	}()

	// Wait for either completion or cancellation
	select {
	case <-cancelChan:
		logger.Info("Processing cancelled during batch processing")
		// Note: The actual processing goroutine will continue, but we'll return an error
		// The processor should check proc.Status periodically to detect cancellation
		return fmt.Errorf("processing cancelled by user")
	case err := <-done:
		if err != nil {
			return fmt.Errorf("processing error: %w", err)
		}
	}

	logger.Info(fmt.Sprintf("Completed processing %d files", len(files)))
	return nil
}
