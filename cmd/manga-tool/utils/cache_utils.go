package utils

import (
	"fmt"
	"path/filepath"
	"time"

	"manga-tool/internal"
	"manga-tool/internal/util"
)

// WarmupRcloneCache warms up the rclone cache for a manga title
func WarmupRcloneCache(proc *internal.Process, mangaTitle string, logger util.Logger, specificFiles []string, cancelChan chan struct{}, forceCancelChan chan struct{}, rcloneMount string) {
	if proc == nil {
		logger.Error("No active process found for warming cache")
		return
	}

	// Set the cancel functions
	proc.CancelFunc = func() { close(cancelChan) }
	proc.ForceCancel = func() { close(forceCancelChan) }

	// Initialize counters
	totalFiles := 0
	processedFiles := 0

	// Check for cancellation at the start
	select {
	case <-cancelChan:
		proc.Update(0, 0, "Cache warming cancelled")
		logger.Info("Cache warming cancelled")
		return
	case <-forceCancelChan:
		proc.Update(0, 0, "Cache warming force cancelled")
		logger.Info("Cache warming force cancelled")
		return
	default:
	}

	// Start the timer
	startTime := time.Now()

	// Determine the path to the manga directory
	mangaPath := filepath.Join(rcloneMount, mangaTitle)

	// Check if we're looking for specific files
	if len(specificFiles) > 0 {
		logger.Info(fmt.Sprintf("Warming cache for %d specific file(s) in %s", len(specificFiles), mangaTitle))
		totalFiles = len(specificFiles)
		proc.Update(0, totalFiles, fmt.Sprintf("Warming cache for %d specific files", totalFiles))

		// Process each specific file
		for i, file := range specificFiles {
			// Check for cancellation
			select {
			case <-cancelChan:
				proc.Update(processedFiles, totalFiles, "Cache warming cancelled")
				logger.Info("Cache warming cancelled")
				return
			case <-forceCancelChan:
				proc.Update(processedFiles, totalFiles, "Cache warming force cancelled")
				logger.Info("Cache warming force cancelled")
				return
			default:
			}

			// Get the full path to the file
			filePath := filepath.Join(mangaPath, file)
			logger.Info(fmt.Sprintf("Warming cache for specific file (%d/%d): %s", i+1, totalFiles, filePath))
			proc.Update(i, totalFiles, fmt.Sprintf("Warming cache for file %d/%d: %s", i+1, totalFiles, file))

			// Force the file to be cached
			result := util.ForceRcloneCache(filePath, true, logger)
			if result {
				logger.Info(fmt.Sprintf("Successfully warmed cache for: %s", filePath))
			} else {
				logger.Warning(fmt.Sprintf("Failed to warm cache for: %s", filePath))
			}

			// Increment processed count
			processedFiles++
		}
	} else {
		// Find all CBZ files in the manga directory
		logger.Info(fmt.Sprintf("Finding all CBZ files in %s", mangaPath))
		cbzFiles, err := util.FindCBZFilesFromMount(mangaPath)
		if err != nil {
			logger.Error(fmt.Sprintf("Error finding CBZ files: %v", err))
			proc.Update(0, 0, fmt.Sprintf("Error finding CBZ files: %v", err))
			return
		}

		totalFiles = len(cbzFiles)
		logger.Info(fmt.Sprintf("Found %d CBZ files to warm cache for", totalFiles))

		if totalFiles == 0 {
			logger.Info("No files found to warm cache for")
			proc.Update(0, 0, "No files found to warm cache")
			return
		}

		proc.Update(0, totalFiles, fmt.Sprintf("Warming cache for %d files", totalFiles))

		// Process each CBZ file
		for i, file := range cbzFiles {
			// Check for cancellation
			select {
			case <-cancelChan:
				proc.Update(processedFiles, totalFiles, "Cache warming cancelled")
				logger.Info("Cache warming cancelled")
				return
			case <-forceCancelChan:
				proc.Update(processedFiles, totalFiles, "Cache warming force cancelled")
				logger.Info("Cache warming force cancelled")
				return
			default:
			}

			// Get just the filename without the path
			fileName := filepath.Base(file)
			logger.Info(fmt.Sprintf("Warming cache for file (%d/%d): %s", i+1, totalFiles, fileName))
			proc.Update(i, totalFiles, fmt.Sprintf("Warming cache for file %d/%d: %s", i+1, totalFiles, fileName))

			// Force the file to be cached
			result := util.ForceRcloneCache(file, true, logger)
			if result {
				logger.Info(fmt.Sprintf("Successfully warmed cache for: %s", fileName))
			} else {
				logger.Warning(fmt.Sprintf("Failed to warm cache for: %s", fileName))
			}

			// Increment processed count
			processedFiles++
		}
	}

	// Calculate how long it took
	elapsed := time.Since(startTime)
	logger.Info(fmt.Sprintf("Cache warming completed for %s - processed %d files in %v", mangaTitle, totalFiles, elapsed))
	proc.Update(totalFiles, totalFiles, fmt.Sprintf("Cache warming completed - processed %d files", totalFiles))
}
