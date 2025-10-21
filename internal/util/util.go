package util

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"manga-tool/internal/realdebrid"
)

// Constants
var (
	// Regular expressions for identifying manga files
	ChapterPattern       = regexp.MustCompile(`(?i)(?:chapter|ch[\._\s-]?|c[\._\s-]?)(\d+(?:\.\d+)?)`)
	FolderChapterPattern = regexp.MustCompile(`(?i)(?:chapter|ch|c)[.\s_-]*(\d+(?:\.\d+)?)(?:\s|-|$|[/\\])`)
	VolumePattern        = regexp.MustCompile(`(?i)v0*(\d+(?:\.\d+)?)|volume\s+(\d+(?:\.\d+)?)`)
	DoublePagePattern    = regexp.MustCompile(`(?i)p\d+\s*-\s*\d+`)

	// File extensions
	ImageExtensions = []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}
	RarExtensions   = []string{".rar", ".cbr"}
	ZipExtensions   = []string{".zip", ".cbz"}
)

// Logger interface for handling logs
type Logger interface {
	Info(msg string)
	Warning(msg string)
	Error(msg string)
}

// SimpleLogger is a basic logger that outputs to stdout via a log function
type SimpleLogger struct {
	ProcessID string
	LogFunc   func(level, message string)
}

// NewSimpleLogger creates a new simple logger
func NewSimpleLogger(processID string, logFunc func(level, message string)) *SimpleLogger {
	return &SimpleLogger{
		ProcessID: processID,
		LogFunc:   logFunc,
	}
}

// Info logs an informational message
func (l *SimpleLogger) Info(msg string) {
	if l.LogFunc != nil {
		l.LogFunc("INFO", msg)
	}
}

// Warning logs a warning message
func (l *SimpleLogger) Warning(msg string) {
	if l.LogFunc != nil {
		l.LogFunc("WARNING", msg)
	}
}

// Error logs an error message
func (l *SimpleLogger) Error(msg string) {
	if l.LogFunc != nil {
		l.LogFunc("ERROR", msg)
	}
}

// NoopLogger is a logger that does nothing
type NoopLogger struct{}

func (l *NoopLogger) Info(msg string)    {}
func (l *NoopLogger) Warning(msg string) {}
func (l *NoopLogger) Error(msg string)   {}

// LogEntry represents a log entry for the web interface
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// WebInput is a function type that takes a prompt and input type and returns a string
type WebInput func(prompt string, inputType string) string

// IsImageFile checks if a filename has an image extension
func IsImageFile(filename string) bool {
	lowerName := strings.ToLower(filename)
	for _, ext := range ImageExtensions {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

// IsRarFile checks if a filename has a RAR extension
func IsRarFile(filename string) bool {
	lowerName := strings.ToLower(filename)
	for _, ext := range RarExtensions {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

// IsZipFile checks if a filename has a ZIP extension
func IsZipFile(filename string) bool {
	lowerName := strings.ToLower(filename)
	for _, ext := range ZipExtensions {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

// IsMangaFile checks if a filename has a manga archive extension.
func IsMangaFile(filename string) bool {
	return IsZipFile(filename) || IsRarFile(filename)
}

// IsArchive checks if a filename has a known archive extension.
func IsArchive(filename string) bool {
	lowerName := strings.ToLower(filename)
	// Add other archive types if needed
	return IsZipFile(filename) || IsRarFile(filename) || strings.HasSuffix(lowerName, ".7z")
}

// SanitizePath sanitizes a path by replacing invalid characters.
func SanitizePath(path string) string {
	// Replace special characters with underscores
	re := regexp.MustCompile(`[<>:"/\\|?*\t\n]`)
	sanitized := re.ReplaceAllString(path, "_")
	// Trim leading/trailing whitespace
	return strings.TrimSpace(sanitized)
}

// SanitizeForFilesystem sanitizes a string for use in filenames
func SanitizeForFilesystem(title string) string {
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := title
	for _, char := range invalidChars {
		result = strings.ReplaceAll(result, char, "_")
	}
	return strings.TrimSpace(result)
}

// ExtractChapterNumber extracts a chapter number from a filename
func ExtractChapterNumber(filename string) float64 {
	// First check for explicit chapter markers
	match := ChapterPattern.FindStringSubmatch(filename)
	if len(match) > 1 {
		if num, err := strconv.ParseFloat(match[1], 64); err == nil {
			return num
		}
	}

	// For volume files, don't extract numbers in parentheses as they're likely years
	if VolumePattern.MatchString(filename) {
		return 0
	}

	// First, try to match the manga name format: "Manga Name XXX (year)"
	mangaNumRegex := regexp.MustCompile(`(?i)(?:.*?)\s+(\d+(?:\.\d+)?)\s*\(\d{4}\)`)
	mangaMatches := mangaNumRegex.FindStringSubmatch(filename)
	if len(mangaMatches) > 1 {
		num, err := strconv.ParseFloat(mangaMatches[1], 64)
		if err == nil && num > 0 && num < 1900 {
			return num
		}
	}

	// Try the general manga name pattern: "series_name chapter_number"
	generalMangaRegex := regexp.MustCompile(`(?i)(?:.*?)\s+(?:ch(?:apter)?\s+)?(\d+(?:\.\d+)?)`)
	generalMatches := generalMangaRegex.FindStringSubmatch(filename)
	if len(generalMatches) > 1 {
		num, err := strconv.ParseFloat(generalMatches[1], 64)
		if err == nil && num > 0 {
			// Only filter out likely years
			if num < 1900 || num > 2100 {
				return num
			}
		}
	}

	// If that didn't work, try the original standalone number approach
	numberRegex := regexp.MustCompile(`\b(\d+(?:\.\d+)?)\b`)
	matches := numberRegex.FindAllStringSubmatch(filename, -1)
	for _, match := range matches {
		if len(match) > 1 {
			num, err := strconv.ParseFloat(match[1], 64)
			if err == nil && num > 0 {
				// Only filter out likely years
				if num < 1900 || num > 2100 {
					return num
				}
			}
		}
	}

	return 0
}

// ExtractVolumeNumber extracts a volume number from a filename
func ExtractVolumeNumber(filename string) float64 {
	match := VolumePattern.FindStringSubmatch(filename)
	if len(match) > 1 {
		if match[1] != "" {
			if num, err := strconv.ParseFloat(match[1], 64); err == nil {
				return num
			}
		} else if match[2] != "" {
			if num, err := strconv.ParseFloat(match[2], 64); err == nil {
				return num
			}
		}
	}
	return 0
}

// ExtractChapterTitle extracts a chapter title from a folder name
func ExtractChapterTitle(folderName string) string {
	match := regexp.MustCompile(`.*?(?:chapter|ch)\s*\d+\s*[-:]\s*(.*)`).FindStringSubmatch(folderName)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

// FindCBZFiles finds all CBZ files in a directory
func FindCBZFiles(directory string) ([]string, error) {
	var files []string
	var visitedDirs = make(map[string]bool)
	var maxDepth = 10 // Prevent infinite recursion
	var fileCount = 0
	var maxFiles = 5000 // Reasonable limit

	// Normalize path for Linux
	directory = filepath.Clean(directory)

	// Verify directory exists and is accessible
	if _, err := os.Stat(directory); err != nil {
		return nil, fmt.Errorf("directory error: %v", err)
	}

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		// Handle walk errors
		if err != nil {
			return filepath.SkipDir
		}

		// Skip directories we've already seen (symlink loops)
		if info.IsDir() {
			absPath, err := filepath.Abs(path)
			if err != nil {
				return filepath.SkipDir
			}

			if visitedDirs[absPath] {
				return filepath.SkipDir
			}
			visitedDirs[absPath] = true

			// Limit directory depth
			relPath, err := filepath.Rel(directory, path)
			if err == nil {
				depth := len(strings.Split(relPath, string(os.PathSeparator)))
				if depth > maxDepth {
					return filepath.SkipDir
				}
			}
		}

		// Process CBZ files
		if !info.IsDir() && strings.ToLower(filepath.Ext(path)) == ".cbz" {
			files = append(files, path)
			fileCount++

			// Limit total files to avoid memory issues
			if fileCount > maxFiles {
				return fmt.Errorf("too many files (>%d)", maxFiles)
			}
		}
		return nil
	})

	return files, err
}

// ExtractAndDeleteRARs extracts RAR files in a directory and deletes them afterward
// This is now a wrapper around SuperSafeExtractRars to avoid code duplication
func ExtractAndDeleteRARs(directory string, logger Logger) error {
	// Simple progress callback that just passes to logger
	progressCallback := func(msg string) {
		if logger != nil {
			logger.Info(msg)
		}
	}

	// Get parallelism from environment variable or use default 4
	parallelism := 4
	if envVal := os.Getenv("PARALLELISM"); envVal != "" {
		if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
			parallelism = val
		}
	}

	// Call the more complete implementation
	return SuperSafeExtractRars(directory, logger, progressCallback, parallelism)
}

// DownloadFile downloads a file using wget with optional authentication
func DownloadFile(downloadURL, destDir, username, password, realdebridAPIKey, madokamiUsername, madokamiPassword string, logger Logger) error {
	if downloadURL == "" {
		return fmt.Errorf("download URL is empty")
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Downloading from %s", downloadURL))
	}

	// Create the destination directory if it doesn't exist
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	// Check if this is a Madokami link and inject credentials if available
	if strings.Contains(downloadURL, "madokami.al") && madokamiUsername != "" && madokamiPassword != "" {
		if logger != nil {
			logger.Info("Detected Madokami link, injecting authentication credentials")
		}

		// Parse the URL
		parsedURL, err := url.Parse(downloadURL)
		if err == nil {
			// Inject credentials into the URL
			parsedURL.User = url.UserPassword(madokamiUsername, madokamiPassword)
			downloadURL = parsedURL.String()
			if logger != nil {
				// Log without showing password
				logger.Info("Madokami credentials injected into URL")
			}
		} else {
			if logger != nil {
				logger.Warning(fmt.Sprintf("Failed to parse Madokami URL: %v", err))
			}
		}
	}

	// Check if this is a magnet link
	if realdebrid.IsMagnetLink(downloadURL) {
		if logger != nil {
			logger.Info("Detected magnet link, using Real-Debrid for download")
		}

		// Check if Real-Debrid API key is provided
		if realdebridAPIKey == "" {
			return fmt.Errorf("Real-Debrid API key not provided")
		}

		// Create Real-Debrid client
		client := realdebrid.NewClient(realdebridAPIKey)

		// Add magnet to Real-Debrid
		torrentInfo, err := client.AddMagnet(downloadURL)
		if err != nil {
			return fmt.Errorf("failed to add magnet to Real-Debrid: %v", err)
		}

		// Automatically select all files for download
		if err := client.SelectAllFiles(torrentInfo.ID); err != nil {
			if logger != nil {
				logger.Warning(fmt.Sprintf("Failed to auto-select files: %v", err))
			}
		}

		// Wait for download to complete
		for {
			info, err := client.GetTorrentInfo(torrentInfo.ID)
			if err != nil {
				return fmt.Errorf("failed to get torrent info: %v", err)
			}

			if info.Status == "downloaded" {
				break
			}

			if info.Status == "error" {
				return fmt.Errorf("torrent download failed")
			}

			time.Sleep(time.Second)
		}

		// Get direct download link
		directURL, err := client.GetDownloadLink(torrentInfo.ID)
		if err != nil {
			return fmt.Errorf("failed to get download link: %v", err)
		}

		// Log the direct download link for comparison
		if logger != nil {
			logger.Info(fmt.Sprintf("Real-Debrid direct download link: %s", directURL))
		}

		// Update URL to use the direct download link
		downloadURL = directURL
	}

	// Base arguments for wget with progress reporting
	args := []string{
		"--progress=bar:force", // Show progress bar
		"--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		"--content-disposition",
		"--trust-server-names",
		"-c",      // Continue partially downloaded files
		"-t", "3", // Retry 3 times
		"--directory-prefix=" + destDir, // Download to this directory
	}

	// Add authentication if provided
	if username != "" && password != "" {
		if logger != nil {
			logger.Info("Using provided authentication credentials")
		}
		args = append(args, "--user="+username)
		args = append(args, "--password="+password)
	}

	// Add the URL as the last argument
	args = append(args, downloadURL)

	cmd := exec.Command("wget", args...)
	cmd.Dir = destDir

	if logger != nil {
		logger.Info(fmt.Sprintf("Starting download: %s", downloadURL))
		logger.Info("Download progress will be shown below:")
	}

	// Create pipes to capture both stdout and stderr for real-time streaming
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start download: %v", err)
	}

	// Stream stderr (where wget progress goes) in real-time
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if logger != nil && strings.TrimSpace(line) != "" {
				logger.Info(fmt.Sprintf("PROGRESS: %s", line))
			}
		}
	}()

	// Stream stdout in real-time
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if logger != nil && strings.TrimSpace(line) != "" {
				logger.Info(fmt.Sprintf("OUTPUT: %s", line))
			}
		}
	}()

	// Wait for the command to complete
	if err := cmd.Wait(); err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Download failed: %v", err))
		}
		return fmt.Errorf("download failed: %v", err)
	}

	if logger != nil {
		logger.Info("Download completed successfully!")
	}

	return nil
}

// AskToDeleteFilesWithWebInput is a compatibility function for older code
func AskToDeleteFilesWithWebInput(directory string, logger Logger, webInput WebInput) error {
	files, err := FindCBZFiles(directory)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return nil
	}

	// Separate volumes and chapters for better organization
	type FileInfo struct {
		Path   string
		Number float64
	}

	volumes := []FileInfo{}
	chapters := []FileInfo{}

	for _, file := range files {
		baseName := filepath.Base(file)
		volNum := ExtractVolumeNumber(baseName)
		chapterNum := ExtractChapterNumber(baseName)

		// If it's an omnibus or similar, or if no numbers detected, treat as volume 1
		if VolumePattern.MatchString(baseName) || (volNum == 0 && chapterNum == 0) {
			if volNum == 0 {
				volNum = 1
			}
			volumes = append(volumes, FileInfo{Path: file, Number: volNum})
		} else if chapterNum > 0 {
			chapters = append(chapters, FileInfo{Path: file, Number: chapterNum})
		} else {
			// If no chapter number found, also treat as volume 1
			volumes = append(volumes, FileInfo{Path: file, Number: 1})
		}
	}

	// Sort by number
	sortFileInfos := func(files []FileInfo) {
		for i := 0; i < len(files)-1; i++ {
			for j := i + 1; j < len(files); j++ {
				if files[i].Number > files[j].Number {
					files[i], files[j] = files[j], files[i]
				}
			}
		}
	}

	sortFileInfos(volumes)
	sortFileInfos(chapters)

	// Prepare sorted file list
	sortedFiles := []string{}

	// First add all volumes in order
	for _, vol := range volumes {
		sortedFiles = append(sortedFiles, vol.Path)
	}

	// Then add all chapters in order
	for _, ch := range chapters {
		sortedFiles = append(sortedFiles, ch.Path)
	}

	// Create a more explicit file list string with clearer instructions
	var fileList strings.Builder
	fileList.WriteString("DO YOU WANT TO DELETE ANY FILES BEFORE PROCESSING?\n\n")
	fileList.WriteString("Enter file numbers to DELETE (e.g., '1 3 5' or '1-3,5,7-9'), or leave empty to keep all:\n\n")

	if len(volumes) > 0 {
		fileList.WriteString("VOLUMES:\n")
		for i, file := range sortedFiles[:len(volumes)] {
			volNum := ExtractVolumeNumber(filepath.Base(file))
			if volNum == 0 {
				volNum = 1
			}
			fileList.WriteString(fmt.Sprintf("%d. [v%02.0f] %s\n", i+1, volNum, filepath.Base(file)))
		}
	}

	if len(chapters) > 0 {
		if len(volumes) > 0 {
			fileList.WriteString("\n")
		}
		fileList.WriteString("CHAPTERS:\n")
		for i, file := range sortedFiles[len(volumes):] {
			chapterNum := ExtractChapterNumber(filepath.Base(file))
			fileList.WriteString(fmt.Sprintf("%d. [c%04.0f] %s\n", i+len(volumes)+1, chapterNum, filepath.Base(file)))
		}
	}

	// Add a prompt that's very explicit about waiting for user input
	fileList.WriteString("\nWAITING FOR INPUT: Enter file numbers to DELETE or press Enter to keep all files: ")

	// Use the complete file list message as the prompt
	prompt := fileList.String()
	if logger != nil {
		logger.Info("Checking if any files should be deleted before processing...")
	}

	selection := webInput(prompt, "file_deletion")

	if selection == "" {
		if logger != nil {
			logger.Info("User chose to keep all files")
		}
		return nil
	}

	// Parse the selection
	toDelete := map[int]bool{}

	parts := strings.ReplaceAll(selection, " ", ",")
	for _, part := range strings.Split(parts, ",") {
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				if logger != nil {
					logger.Warning(fmt.Sprintf("Skipping invalid range: %s", part))
				}
				continue
			}

			start, err1 := strconv.Atoi(rangeParts[0])
			end, err2 := strconv.Atoi(rangeParts[1])

			if err1 != nil || err2 != nil {
				if logger != nil {
					logger.Warning(fmt.Sprintf("Skipping invalid range: %s", part))
				}
				continue
			}

			for i := start; i <= end; i++ {
				toDelete[i] = true
			}
		} else {
			num, err := strconv.Atoi(part)
			if err != nil {
				if logger != nil {
					logger.Warning(fmt.Sprintf("Skipping invalid number: %s", part))
				}
				continue
			}

			toDelete[num] = true
		}
	}

	// Delete the selected files
	deletedCount := 0
	for i, file := range sortedFiles {
		if toDelete[i+1] {
			if err := os.Remove(file); err != nil {
				if logger != nil {
					logger.Error(fmt.Sprintf("Error deleting %s: %v", filepath.Base(file), err))
				}
			} else {
				if logger != nil {
					logger.Info(fmt.Sprintf("Deleted: %s", filepath.Base(file)))
				}
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		if logger != nil {
			logger.Info(fmt.Sprintf("Deleted %d file(s)", deletedCount))
		}
	} else {
		if logger != nil {
			logger.Info("No files were deleted")
		}
	}

	return nil
}

// CopyFile copies a file from src to dst
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return err
	}

	return dstFile.Sync()
}

// DirExists checks if a directory exists
func DirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// GetDirPermissions returns the permissions of a directory as a string
func GetDirPermissions(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return err.Error()
	}
	return info.Mode().String()
}

// CopyFileToRclone copies a file from local disk to an rclone mount
// with special handling for potential issues with rclone
func CopyFileToRclone(src, dst string, logger Logger) error {
	// Read source file
	sourceContent, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("error reading source file: %v", err)
	}

	// Ensure destination directory exists
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("error creating destination directory: %v", err)
	}

	// Use WriteFile instead of Copy to avoid potential rclone issues
	if err := os.WriteFile(dst, sourceContent, 0644); err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Failed to write file to rclone mount: %v", err))
		}
		return fmt.Errorf("error writing to destination: %v", err)
	}

	// Verify file was written correctly (optional but helps ensure data integrity)
	if fi, err := os.Stat(dst); err != nil {
		return fmt.Errorf("error verifying destination file: %v", err)
	} else if fi.Size() != int64(len(sourceContent)) {
		return fmt.Errorf("destination file size mismatch: expected %d, got %d", len(sourceContent), fi.Size())
	}

	return nil
}

// BatchCopyToRclone copies multiple files to an rclone mount
func BatchCopyToRclone(sourceDir, destDir string, logger Logger, parallelism int) error {
	if logger != nil {
		logger.Info(fmt.Sprintf("Starting batch copy to rclone mount: %s", destDir))
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %v", err)
	}

	// Get all files in the source directory
	files, err := filepath.Glob(filepath.Join(sourceDir, "*"))
	if err != nil {
		return fmt.Errorf("error finding source files: %v", err)
	}

	if len(files) == 0 {
		if logger != nil {
			logger.Warning("No files found to copy to rclone mount")
		}
		return nil
	}

	// Create channels for job distribution
	jobs := make(chan string, len(files))
	results := make(chan error, len(files))

	// Number of workers for parallel processing
	numWorkers := parallelism
	if numWorkers <= 0 {
		numWorkers = 4 // Default to 4 if invalid value
	}

	// Start workers
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range jobs {
				fileName := filepath.Base(file)
				destPath := filepath.Join(destDir, fileName)

				if logger != nil {
					logger.Info(fmt.Sprintf("Copying file to rclone: %s", fileName))
				}

				err := CopyFileToRclone(file, destPath, logger)
				if err != nil {
					if logger != nil {
						logger.Error(fmt.Sprintf("Failed to copy %s: %v", fileName, err))
					}
					results <- err
				} else {
					results <- nil

					// Remove the source file after successful copy
					if err := os.Remove(file); err != nil && logger != nil {
						logger.Warning(fmt.Sprintf("Could not remove source file %s after copy: %v", fileName, err))
					}
				}
			}
		}()
	}

	// Send jobs to workers
	go func() {
		for _, file := range files {
			jobs <- file
		}
		close(jobs)
	}()

	// Collect results
	successCount := 0
	errorCount := 0
	for i := 0; i < len(files); i++ {
		if err := <-results; err != nil {
			errorCount++
		} else {
			successCount++
		}
	}

	// Wait for all workers to finish
	wg.Wait()

	if logger != nil {
		logger.Info(fmt.Sprintf("Batch copy complete: %d files copied, %d errors", successCount, errorCount))
	}

	if errorCount > 0 {
		return fmt.Errorf("%d files failed to copy to rclone mount", errorCount)
	}

	return nil
}

// IsRcloneMount checks if a directory is (likely) an rclone mount
func IsRcloneMount(path string) bool {
	// Read mountinfo to check if this is a FUSE mount
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}

	// Convert to string and check if path is listed as a mount
	mountInfoStr := string(mountInfo)
	return strings.Contains(mountInfoStr, path)
}

// DeleteFileWithRetry attempts to delete a file with retries, useful for rclone mounts
func DeleteFileWithRetry(path string, maxRetries int, logger Logger) error {
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		err := os.Remove(path)
		if err == nil {
			return nil
		}

		lastErr = err

		// Log retry attempt
		if logger != nil && i < maxRetries-1 {
			logger.Info(fmt.Sprintf("Retry %d/%d deleting file %s: %v",
				i+1, maxRetries, filepath.Base(path), err))
		}

		// Wait before retry
		time.Sleep(time.Duration(100*(i+1)) * time.Millisecond)
	}

	return fmt.Errorf("failed to delete file after %d attempts: %v", maxRetries, lastErr)
}

// GetEnv retrieves an environment variable or returns a default value
func GetEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// GetEnvInt retrieves an environment variable as an integer or returns a default value
func GetEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// ForceRcloneCache forces a file to be cached by rclone
func ForceRcloneCache(path string, shouldExist bool, logger Logger) bool {
	// Check if the file exists
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		if shouldExist {
			if logger != nil {
				logger.Warning(fmt.Sprintf("File does not exist: %s", path))
			}
			return false
		}
	} else if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error accessing file %s: %v", path, err))
		}
		return false
	}

	// Try to read the file to force caching
	file, err := os.Open(path)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error opening file %s: %v", path, err))
		}
		return false
	}
	defer file.Close()

	// Read a small amount to trigger cache
	buffer := make([]byte, 4096)
	_, err = file.Read(buffer)
	if err != nil && err != io.EOF {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error reading file %s: %v", path, err))
		}
		return false
	}

	return true
}

// FindCBZFilesFromMount finds all CBZ files in a mounted directory
func FindCBZFilesFromMount(dir string) ([]string, error) {
	var files []string

	// Walk the directory
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Check if it's a regular file with .cbz extension
		if !info.IsDir() && strings.ToLower(filepath.Ext(path)) == ".cbz" {
			files = append(files, path)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return files, nil
}
