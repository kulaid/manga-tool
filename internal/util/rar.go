package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ExtractRarFileSafely extracts a single RAR file with extra safety measures
// Now adds progress reporting functionality and only uses unrar with speed optimization
func ExtractRarFileSafely(rarFile string, logger Logger) bool {
	// Use a defer/recover to catch any panics
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("PANIC during RAR extraction: %v", r))
			}
		}
	}()

	if logger != nil {
		logger.Info(fmt.Sprintf("Extracting: %s", filepath.Base(rarFile)))
	}

	// Extract to the same directory as the RAR file
	extractDir := filepath.Dir(rarFile)

	// Check file size for progress estimation
	fileInfo, err := os.Stat(rarFile)
	var fileSize int64
	if err == nil {
		fileSize = fileInfo.Size()
		if logger != nil && fileSize > 0 {
			logger.Info(fmt.Sprintf("File size: %.2f MB", float64(fileSize)/(1024*1024)))
		}
	}

	// Create command with optimized flags for speed
	// -y: Yes to all queries (overwrite)
	// -mt: Set number of threads (use 4 for good balance of speed vs stability)
	// -inul: Disable percentage indicator (reduces stdout overhead)
	// -idp: Disable percentage indicator (redundant but ensures maximum compatibility)
	// -o+: Overwrite existing files
	cmd := exec.Command("unrar", "x", "-y", "-mt4", "-o+", rarFile)
	cmd.Dir = extractDir

	// Use real-time output monitoring
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	err = cmd.Start()
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to start unrar: %v", err))
		}
		return false
	}

	// Read output to monitor progress
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				outStr := string(buf[:n])
				// Look for progress indicators in the output
				if strings.Contains(outStr, "%") && logger != nil {
					// Clean up the output for better readability
					cleanOutput := strings.TrimSpace(outStr)
					// Try to extract percentage from unrar output
					percentIndex := strings.LastIndex(cleanOutput, "%")
					if percentIndex > 0 && percentIndex < len(cleanOutput) {
						// Look for a number before the % sign
						start := percentIndex
						for start > 0 && (cleanOutput[start-1] >= '0' && cleanOutput[start-1] <= '9' || cleanOutput[start-1] == '.') {
							start--
						}
						if start < percentIndex {
							percentStr := cleanOutput[start:percentIndex]
							// Log the extraction progress
							logger.Info(fmt.Sprintf("Extraction progress: %s%%", percentStr))

							// Update process status if we have a progress callback
							if progressCallback, ok := logger.(interface{ UpdateProgress(int) }); ok {
								// Convert percentage string to int for process update
								if percent, err := strconv.Atoi(percentStr); err == nil {
									progressCallback.UpdateProgress(percent)
								}
							}
						} else {
							logger.Info(fmt.Sprintf("Extraction progress: %s", cleanOutput))
						}
					} else {
						logger.Info(fmt.Sprintf("Extraction progress: %s", cleanOutput))
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Also read stderr for any error messages
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 && logger != nil {
				logger.Warning(fmt.Sprintf("unrar stderr: %s", strings.TrimSpace(string(buf[:n]))))
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for the command to complete
	err = cmd.Wait()
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("unrar extraction failed: %v", err))
		}
		return false
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Successfully extracted %s", filepath.Base(rarFile)))
	}
	return true
}

// SuperSafeExtractRars extracts multiple RAR files safely, with concurrency control and progress tracking
func SuperSafeExtractRars(directory string, logger Logger, progressCallback func(string), parallelism int) error {
	// Get list of RAR files in the directory
	var rarFiles []string
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("failed to read directory: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filePath := filepath.Join(directory, entry.Name())
		if IsRarFile(filePath) {
			rarFiles = append(rarFiles, filePath)
		}
	}

	if len(rarFiles) == 0 {
		if logger != nil {
			logger.Info("No RAR files found to extract")
		}
		return nil
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d RAR files to extract", len(rarFiles)))
	}

	// Calculate total size for progress tracking
	var totalSize int64
	for _, file := range rarFiles {
		info, err := os.Stat(file)
		if err == nil {
			totalSize += info.Size()
		}
	}

	if totalSize > 0 && logger != nil {
		logger.Info(fmt.Sprintf("Total size to extract: %.2f MB", float64(totalSize)/(1024*1024)))
	}

	// Determine the optimal number of concurrent extractions based on parallelism setting
	maxConcurrent := parallelism
	if maxConcurrent <= 0 {
		maxConcurrent = 4 // Default to 4 concurrent extractions
	}

	// Process RAR files with controlled concurrency
	var extractedSize int64
	semaphore := make(chan bool, maxConcurrent)
	errorChan := make(chan error, len(rarFiles))
	resultChan := make(chan struct {
		index    int
		fileSize int64
		success  bool
		fileName string
	}, len(rarFiles))

	// Start extraction of each RAR file in parallel (with limits)
	for i, rarFile := range rarFiles {
		// Get file size for progress calculation
		fileInfo, err := os.Stat(rarFile)
		fileSize := int64(0)
		if err == nil {
			fileSize = fileInfo.Size()
		}

		// Update initial progress
		fileName := filepath.Base(rarFile)
		progressMsg := fmt.Sprintf("Starting extraction of file %d of %d: %s",
			i+1, len(rarFiles), fileName)

		if progressCallback != nil {
			progressCallback(progressMsg)
		}

		if logger != nil {
			logger.Info(progressMsg)
		}

		// Start extraction in a goroutine
		go func(index int, file string, size int64) {
			// Acquire semaphore slot
			semaphore <- true
			defer func() { <-semaphore }() // Release when done

			// Extract with safety wrapper
			success := ExtractRarFileSafely(file, logger)

			// Send result
			resultChan <- struct {
				index    int
				fileSize int64
				success  bool
				fileName string
			}{
				index:    index,
				fileSize: size,
				success:  success,
				fileName: filepath.Base(file),
			}
		}(i, rarFile, fileSize)
	}

	// Collect results and handle file deletion
	for i := 0; i < len(rarFiles); i++ {
		result := <-resultChan

		// Update progress
		extractedSize += result.fileSize
		overallPercent := 0
		if totalSize > 0 {
			overallPercent = int((extractedSize * 100) / totalSize)
		}

		progressMsg := fmt.Sprintf("Completed file %d of %d: %s - Overall progress: %d%%",
			result.index+1, len(rarFiles), result.fileName, overallPercent)

		if progressCallback != nil {
			progressCallback(progressMsg)
		}

		if logger != nil {
			logger.Info(progressMsg)
		}

		// Handle file deletion for successful extractions
		if result.success {
			// Try to delete but don't crash if it fails
			if err := os.Remove(rarFiles[result.index]); err != nil {
				if logger != nil {
					logger.Warning(fmt.Sprintf("Failed to delete %s: %v", result.fileName, err))
				}
			} else {
				if logger != nil {
					logger.Info(fmt.Sprintf("Deleted RAR file: %s", result.fileName))
				}
			}
		} else {
			if logger != nil {
				logger.Warning(fmt.Sprintf("Skipping deletion of %s due to extraction failure", result.fileName))
			}
			errorChan <- fmt.Errorf("failed to extract %s", result.fileName)
		}
	}

	// Check if there were any errors
	close(errorChan)
	var extractErrors []error
	for err := range errorChan {
		extractErrors = append(extractErrors, err)
	}

	// Final progress update
	if progressCallback != nil {
		if len(extractErrors) > 0 {
			progressCallback(fmt.Sprintf("Completed extraction with %d errors", len(extractErrors)))
		} else {
			progressCallback(fmt.Sprintf("Successfully extracted all %d files", len(rarFiles)))
		}
	}

	// Return error if any extraction failed
	if len(extractErrors) > 0 {
		return fmt.Errorf("encountered %d extraction errors", len(extractErrors))
	}

	return nil
}

// No need to redefine IsRarFile - it's already in util.go
// Also removing ExtractAndDeleteRARs as it's already in util.go
