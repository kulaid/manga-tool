package processor

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"manga-tool/internal"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	// Uncomment these imports to enable performance profiling
	// _ "net/http/pprof"
	// "runtime/pprof"
	// "net/http"
)

// Constants for regexes
var (
	chapterRegex    = regexp.MustCompile(`(?i)(?:chapter|ch[\._\s-]?|c[\._\s-]?)(\d+(?:\.\d+)?)`)
	volumeRegex     = regexp.MustCompile(`(?i)v0*(\d+)|volume\s+(\d+)`)
	doublePageRegex = regexp.MustCompile(`(?i)p\d+\s*-\s*\d+`)
	imageExtensions = []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}
)

/*
Performance Profiling Guide:

To enable performance profiling:
1. Uncomment the imports at the top of this file
2. Add the following code to your main function:

   go func() {
       http.ListenAndServe("localhost:6060", nil)
   }()

3. Run your application and navigate to http://localhost:6060/debug/pprof in a browser
4. For CPU profiling: http://localhost:6060/debug/pprof/profile
5. For memory profiling: http://localhost:6060/debug/pprof/heap
6. For goroutine analysis: http://localhost:6060/debug/pprof/goroutine

This will help identify performance bottlenecks and memory usage patterns.
*/

// ComicInfo represents the ComicInfo.xml structure
type ComicInfo struct {
	XMLName          xml.Name `xml:"ComicInfo"`
	Series           string   `xml:"Series"`
	Number           string   `xml:"Number"`
	Volume           string   `xml:"Volume,omitempty"`
	Chapter          string   `xml:"Chapter,omitempty"`
	Title            string   `xml:"Title,omitempty"`
	Manga            string   `xml:"Manga"`
	LanguageISO      string   `xml:"LanguageISO"`
	ReadingDirection string   `xml:"ReadingDirection,omitempty"`
	DoublePage       string   `xml:"DoublePage,omitempty"`
}

// ProcessingStatus represents the current processing status
type ProcessingStatus struct {
	Active   bool
	Progress int
	Total    int
	Message  string
	Error    string
}

// Logger interface for handling logs
type Logger interface {
	Info(msg string)
	Warning(msg string)
	Error(msg string)
}

// Config contains options for processing
type Config struct {
	MangaBaseDir    string
	TempDir         string
	ChapterTitles   map[float64]string
	DeleteOriginals bool
	IsManga         bool
	IsOneshot       bool // New field for oneshot flag
	Language        string
	Parallelism     int // Number of concurrent workers (0 = use NumCPU)
	Logger          Logger
	Process         *internal.Process               // Replace Status with Process
	GetChapterFunc  func(chapterNum float64) string // Function to get chapter title by number
}

// DefaultConfig creates a default configuration
func DefaultConfig() *Config {
	return &Config{
		ChapterTitles:   make(map[float64]string),
		DeleteOriginals: true,
		IsManga:         true,
		IsOneshot:       false,
		Language:        "en",
		Parallelism:     0, // Use CPU count automatically
	}
}

// isImageFile checks if a filename is an image
func isImageFile(filename string) bool {
	lowerName := strings.ToLower(filename)
	for _, ext := range imageExtensions {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

// sanitizeForFilesystem sanitizes a string for filesystem use
func sanitizeForFilesystem(name string) string {
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := name
	for _, char := range invalidChars {
		result = strings.ReplaceAll(result, char, "_")
	}
	return strings.TrimSpace(result)
}

// extractVolumeNumber extracts volume number from a filename
func extractVolumeNumber(filename string) int {
	matches := volumeRegex.FindStringSubmatch(filename)
	if len(matches) > 1 {
		if matches[1] != "" {
			if num, err := strconv.Atoi(matches[1]); err == nil {
				return num
			}
		} else if matches[2] != "" {
			if num, err := strconv.Atoi(matches[2]); err == nil {
				return num
			}
		}
	}
	return 0
}

func extractChapterNumber(filename string) float64 {
	// 1. Check for an explicit chapter marker (e.g. "ch123" or "c123").
	if matches := chapterRegex.FindStringSubmatch(filename); len(matches) > 1 {
		if num, err := strconv.ParseFloat(matches[1], 64); err == nil {
			return num
		}
	}

	// 2. If the filename looks like a volume (contains 'v' or "volume"), skip chapter extraction.
	if volumeRegex.MatchString(filename) {
		return 0
	}

	// 3. Try to match a "manga name" format like "Some Title 123 (2024)".
	mangaRegex := regexp.MustCompile(`(?i)(?:.*?)\s+(\d+(?:\.\d+)?)\s*\(\d{4}\)`)
	if matches := mangaRegex.FindStringSubmatch(filename); len(matches) > 1 {
		if num, err := strconv.ParseFloat(matches[1], 64); err == nil && num > 0 && num < 1900 {
			return num
		}
	}

	// 4. Fallback: use a general pattern for a number in the filename.
	//    This pattern simply looks for a number with 1-4 digits and an optional fraction.
	generalRegex := regexp.MustCompile(`(?i)\b0*(\d{1,4}(?:\.\d+)?)\b`)
	if matches := generalRegex.FindStringSubmatch(filename); len(matches) > 1 {
		if num, err := strconv.ParseFloat(matches[1], 64); err == nil && num >= 0 {
			// Avoid numbers that look like years (but allow 0 for Chapter 0).
			if num < 1900 || num > 2100 {
				return num
			}
		}
	}

	return 0
}

// getChapterTitle gets the corresponding title for a chapter number
func getChapterTitle(chapterNum float64, config *Config) string {
	// Check for nil values
	if config == nil || config.ChapterTitles == nil {
		return fmt.Sprintf("Chapter %g", chapterNum)
	}

	// Look up the title in our map
	if title, ok := config.ChapterTitles[chapterNum]; ok && title != "" {
		if config.Logger != nil {
			config.Logger.Info(fmt.Sprintf("Found title for Chapter %g: %s", chapterNum, title))
		}
		return title
	}

	// Try using the GetChapterFunc if available
	if config.GetChapterFunc != nil {
		if title := config.GetChapterFunc(chapterNum); title != "" {
			// Save the title in our map for future reference
			config.ChapterTitles[chapterNum] = title
			if config.Logger != nil {
				config.Logger.Info(fmt.Sprintf("Got title for Chapter %g from user: %s", chapterNum, title))
			}
			return title
		}
	}

	// If no title found, log a warning and return a default
	if config.Logger != nil {
		config.Logger.Warning(fmt.Sprintf("No title found for Chapter %g, using default", chapterNum))
	}
	return fmt.Sprintf("Chapter %g", chapterNum)
}

// createComicInfoXML creates ComicInfo.xml content for a chapter or volume
func createComicInfoXML(fileType string, number float64, seriesName string, volumeNumber int, doublePages bool, chapterTitle string, config *Config) ([]byte, error) {
	info := ComicInfo{
		Series:      seriesName,
		Manga:       "Yes",
		LanguageISO: config.Language,
	}

	if config.IsManga {
		info.ReadingDirection = "RightToLeft"
	} else {
		info.Manga = "No"
	}

	if doublePages {
		info.DoublePage = "Yes"
	}

	if fileType == "chapter" {
		// Handle oneshot special case - force to volume 1, chapter 1
		if config.IsOneshot {
			info.Number = "1"
			info.Chapter = "1"
			info.Volume = "1"
			// For oneshots, use the chapter title from the map (which should be the manga title)
			if title, exists := config.ChapterTitles[1.0]; exists {
				info.Title = title
			} else {
				info.Title = chapterTitle
			}
		} else {
			info.Number = fmt.Sprintf("%g", number)  // Remove trailing zeros for display
			info.Chapter = fmt.Sprintf("%g", number) // Add chapter number
			// Add volume information if available
			if volumeNumber > 0 {
				info.Volume = fmt.Sprintf("%d", volumeNumber)
			}
			if chapterTitle == "" {
				// Need to convert float to int for lookup if it's a whole number
				if number == float64(int(number)) {
					chapterTitle = getChapterTitle(number, config)
				} else {
					// For fractional chapters, format as string first
					chapterNumStr := fmt.Sprintf("%g", number)
					chapterTitle = fmt.Sprintf("Chapter %s", chapterNumStr)
					// Try to find title for fractional chapter
					for k, v := range config.ChapterTitles {
						// Compare as strings to handle floating point precision issues
						if fmt.Sprintf("%g", k) == chapterNumStr {
							chapterTitle = v
							break
						}
					}
				}
			}
			info.Title = chapterTitle
		}
	} else {
		// For volumes, set Number to "0.01" format for Komga compatibility where 0.01 is volume 1
		info.Number = fmt.Sprintf("0.%02d", volumeNumber)
		info.Volume = fmt.Sprintf("%d", volumeNumber)
		// For volume files, chapter is same as volume
		info.Chapter = fmt.Sprintf("%d", volumeNumber)
		info.Title = fmt.Sprintf("Volume %d", volumeNumber)
	}

	output, err := xml.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}

	// Add XML header
	return []byte(xml.Header + string(output)), nil
}

// extractZip extracts a ZIP file to a directory
func extractZip(zipPath, targetDir string) error {
	// Open the ZIP file
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("error opening ZIP file: %v", err)
	}
	defer r.Close()

	// Calculate the optimal number of workers based on CPU count
	concurrency := runtime.NumCPU() * 2
	if concurrency > 16 {
		concurrency = 16 // Cap at 16 to avoid excessive concurrency
	}

	// Create semaphore for limiting concurrency
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	// Use atomics for tracking errors
	var errCount int32
	var firstErr error
	var errMu sync.Mutex

	// Preallocate important directories - this avoids contention during extraction
	seenDirs := make(map[string]struct{})
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}

		// Extract directory path
		dirPath := filepath.Dir(filepath.Join(targetDir, f.Name))
		if _, ok := seenDirs[dirPath]; !ok {
			seenDirs[dirPath] = struct{}{}

			// Create directory now to avoid parallel creation issues
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %v", dirPath, err)
			}
		}
	}

	// Process files in batches
	batchSize := 50
	fileCount := len(r.File)
	for i := 0; i < fileCount; i += batchSize {
		end := i + batchSize
		if end > fileCount {
			end = len(r.File)
		}

		batch := r.File[i:end]

		// Start workers for this batch
		for _, file := range batch {
			// Skip directories
			if file.FileInfo().IsDir() {
				continue
			}

			// Skip non-image/non-xml files for efficiency
			if !isImageFile(file.Name) && !strings.HasSuffix(strings.ToLower(file.Name), ".xml") {
				continue
			}

			wg.Add(1)
			sem <- struct{}{} // Acquire token

			go func(f *zip.File) {
				defer wg.Done()
				defer func() { <-sem }() // Release token

				// Calculate target path
				destPath := filepath.Join(targetDir, f.Name)

				// Open the file from the ZIP
				rc, err := f.Open()
				if err != nil {
					errMu.Lock()
					if atomic.AddInt32(&errCount, 1) == 1 {
						firstErr = fmt.Errorf("failed to open file in ZIP: %v", err)
					}
					errMu.Unlock()
					return
				}
				defer rc.Close()

				// Create the destination file
				destFile, err := os.Create(destPath)
				if err != nil {
					errMu.Lock()
					if atomic.AddInt32(&errCount, 1) == 1 {
						firstErr = fmt.Errorf("failed to create destination file: %v", err)
					}
					errMu.Unlock()
					return
				}
				defer destFile.Close()

				// Use optimal buffer size based on file size
				var bufSize int
				if f.UncompressedSize64 > 10*1024*1024 { // > 10MB
					bufSize = 8 * 1024 * 1024 // 8MB buffer for large files
				} else if f.UncompressedSize64 > 1024*1024 { // > 1MB
					bufSize = 4 * 1024 * 1024 // 4MB buffer for medium files
				} else {
					bufSize = 1 * 1024 * 1024 // 1MB buffer for small files
				}

				// Use buffered writers for better performance
				bufWriter := bufio.NewWriterSize(destFile, 8*1024*1024) // 8MB write buffer
				buf := make([]byte, bufSize)

				// Copy the contents with the buffer
				if _, err := io.CopyBuffer(bufWriter, rc, buf); err != nil {
					errMu.Lock()
					if atomic.AddInt32(&errCount, 1) == 1 {
						firstErr = fmt.Errorf("failed to copy file contents: %v", err)
					}
					errMu.Unlock()
					return
				}

				// Flush the buffer - critical to ensure data is written
				if err := bufWriter.Flush(); err != nil {
					errMu.Lock()
					if atomic.AddInt32(&errCount, 1) == 1 {
						firstErr = fmt.Errorf("failed to flush buffer: %v", err)
					}
					errMu.Unlock()
					return
				}
			}(file)
		}

		// Wait for current batch to finish before starting next batch
		wg.Wait()
	}

	// Check if there were any errors
	if errCount > 0 {
		return fmt.Errorf("encountered %d errors during extraction, first error: %v",
			errCount, firstErr)
	}

	return nil
}

// createCBZ creates a CBZ file from a directory
func createCBZ(sourceDir, outputPath string) error {
	// Create output file
	zipFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("error creating output file: %v", err)
	}
	defer zipFile.Close()

	// Create a buffered writer with larger 16MB buffer for better I/O performance
	bufferedWriter := bufio.NewWriterSize(zipFile, 16*1024*1024)
	defer bufferedWriter.Flush()

	// Create a new zip writer with optimal compression levels
	zipWriter := zip.NewWriter(bufferedWriter)
	defer zipWriter.Close()

	// Get optimal compression workers based on CPU count - use more workers
	concurrency := runtime.NumCPU() * 2
	if concurrency > 16 {
		concurrency = 16 // Cap at 16 to avoid diminishing returns
	}

	// Semaphore to limit concurrent file operations
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var zipMutex sync.Mutex
	var errOnce sync.Once
	var retErr error

	// Get file list before processing to avoid directory traversal during zip creation
	var files []string
	err = filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Sort files first for better compression
	sort.Strings(files)

	// Process files in batches - reading and compressing small groups together
	batchSize := 50
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}

		currentBatch := files[i:end]
		for _, fp := range currentBatch {
			// Skip the loop iteration for non-image files that aren't XML
			if !isImageFile(fp) && !strings.HasSuffix(strings.ToLower(fp), ".xml") {
				continue
			}

			wg.Add(1)
			sem <- struct{}{} // Acquire semaphore
			go func(filePath string) {
				defer wg.Done()

				// Get file info
				info, err := os.Stat(filePath)
				if err != nil {
					errOnce.Do(func() { retErr = fmt.Errorf("stat file %s: %w", filePath, err) })
					<-sem
					return
				}

				relPath, err := filepath.Rel(sourceDir, filePath)
				if err != nil {
					errOnce.Do(func() { retErr = fmt.Errorf("rel path for %s: %w", filePath, err) })
					<-sem
					return
				}

				// Optimize buffer size based on file size
				var bufSize int
				if info.Size() > 10*1024*1024 { // > 10MB
					bufSize = 8 * 1024 * 1024 // 8MB buffer
				} else if info.Size() > 1024*1024 { // > 1MB
					bufSize = 4 * 1024 * 1024 // 4MB buffer
				} else {
					bufSize = 1 * 1024 * 1024 // 1MB buffer
				}

				// Read file with buffered reader
				file, err := os.Open(filePath)
				if err != nil {
					errOnce.Do(func() { retErr = fmt.Errorf("open file %s: %w", filePath, err) })
					<-sem
					return
				}

				// Use buffered reader with appropriate buffer size
				reader := bufio.NewReaderSize(file, bufSize)
				content, err := io.ReadAll(reader)
				file.Close() // Close file immediately after reading

				if err != nil {
					errOnce.Do(func() { retErr = fmt.Errorf("reading file %s: %w", filePath, err) })
					<-sem
					return
				}

				// Create zip header
				header, err := zip.FileInfoHeader(info)
				if err != nil {
					errOnce.Do(func() { retErr = fmt.Errorf("creating header for %s: %w", filePath, err) })
					<-sem
					return
				}
				header.Name = filepath.ToSlash(relPath)

				// Use Store (no compression) for all files in CBZ
				// Images are already compressed, and XML files are tiny
				// Store mode is optimal for manga readers and faster processing
				header.Method = zip.Store

				// Acquire lock, create entry, and write content
				zipMutex.Lock()
				writer, err := zipWriter.CreateHeader(header)
				if err != nil {
					zipMutex.Unlock()
					errOnce.Do(func() { retErr = fmt.Errorf("creating zip entry for %s: %w", filePath, err) })
					<-sem
					return
				}
				_, err = writer.Write(content)
				zipMutex.Unlock()

				if err != nil {
					errOnce.Do(func() { retErr = fmt.Errorf("writing content for %s: %w", filePath, err) })
				}

				<-sem // Release semaphore
			}(fp)
		}

		// Wait for each batch to complete before starting the next
		wg.Wait()
		if retErr != nil {
			return retErr
		}
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	// Use a larger 4MB buffer for improved performance
	buf := make([]byte, 4*1024*1024)
	_, err = io.CopyBuffer(out, in, buf)
	if err != nil {
		return err
	}

	return out.Sync()
}

// ProcessVolumeFile processes a volume file by extracting chapters
func ProcessVolumeFile(filePath, outputDir, seriesName string, config *Config) error {
	logger := config.Logger
	baseName := filepath.Base(filePath)
	startTime := time.Now()

	if logger != nil {
		logger.Info(fmt.Sprintf("Starting volume processing: %s", baseName))
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Opening volume file: %s", baseName))
	}

	// Extract volume number
	volNum := extractVolumeNumber(baseName)

	if volNum == 0 {
		if logger != nil {
			logger.Error(fmt.Sprintf("Could not extract volume number from %s", baseName))
		}
		return fmt.Errorf("could not extract volume number from %s", baseName)
	}

	// Open the zip file
	zipReader, err := zip.OpenReader(filePath)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error reading ZIP %s: %v", baseName, err))
		}
		return fmt.Errorf("error reading ZIP %s: %v", baseName, err)
	}
	defer zipReader.Close()

	// Build an index of zip entries for quick lookup and collect image files in one pass
	zipIndex := make(map[string]*zip.File, len(zipReader.File))
	imageFiles := make([]string, 0, len(zipReader.File))

	for _, f := range zipReader.File {
		zipIndex[f.Name] = f

		// Skip ComicInfo.xml and hidden files
		if filepath.Base(f.Name) == "ComicInfo.xml" || strings.HasPrefix(filepath.Base(f.Name), ".") {
			continue
		}

		if isImageFile(f.Name) {
			imageFiles = append(imageFiles, f.Name)
		}
	}

	if len(imageFiles) == 0 {
		if logger != nil {
			logger.Error(fmt.Sprintf("No valid images found in %s", baseName))
		}
		return fmt.Errorf("no valid images found in %s", baseName)
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d image files in volume %d", len(imageFiles), volNum))

		// Debug: List first few images to help determine structure
		if len(imageFiles) > 0 && len(imageFiles) < 20 {
			logger.Info("First few images found:")
			for i := 0; i < min(5, len(imageFiles)); i++ {
				logger.Info(fmt.Sprintf("  - %s", imageFiles[i]))
			}
		}
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Volume %d: Analyzing %d image files for chapter grouping", volNum, len(imageFiles)))
	}

	// Group pages into chapters
	chapterFiles := groupFilesByChapter(imageFiles, logger)

	// If no chapters found or chapters don't make sense, process as single volume
	if len(chapterFiles) == 0 {
		if logger != nil {
			logger.Info(fmt.Sprintf("No valid chapter markers found in %s, processing as a single volume", baseName))
		}

		if config.Process != nil {
			updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Processing %s as a complete volume", baseName))
		}

		return ProcessCBZFile(filePath, "volume", seriesName, volNum, outputDir, config)
	}

	// Check if we have too few chapters for a volume or too many files per chapter
	chapterKeys := make([]float64, 0, len(chapterFiles))
	for k := range chapterFiles {
		chapterKeys = append(chapterKeys, k)
	}
	sort.Float64s(chapterKeys)

	// Typical manga volume has 5-10 chapters - if we find just 1-2, be suspicious
	if len(chapterKeys) < 3 {
		avgFilesPerChapter := len(imageFiles) / len(chapterKeys)
		if avgFilesPerChapter > 50 { // Typical chapter has 20-30 pages
			if logger != nil {
				logger.Warning(fmt.Sprintf("Found only %d chapters with an average of %d files each, which seems unusual. Verify the output is correct.",
					len(chapterKeys), avgFilesPerChapter))
			}
		}
	}

	// If just one "chapter" and it looks like a year, process as single volume
	if len(chapterFiles) == 1 {
		for chapterNum := range chapterFiles {
			if chapterNum >= 1900 && chapterNum <= 2100 {
				if logger != nil {
					logger.Info(fmt.Sprintf("Only found one potential chapter with number %g, which looks like a year. Processing as a single volume.", chapterNum))
				}

				if config.Process != nil {
					updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Processing %s as a complete volume (year number detected)", baseName))
				}

				return ProcessCBZFile(filePath, "volume", seriesName, volNum, outputDir, config)
			}
		}
	}

	// We have valid chapter markers
	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d chapters in volume %d", len(chapterFiles), volNum))
		// Log chapter numbers we found for debugging
		logger.Info(fmt.Sprintf("Chapters found: %v", chapterKeys))
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Volume %d: Processing %d chapters", volNum, len(chapterFiles)))
	}

	// Use a worker pool to process chapters concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0

	// Calculate worker count - default to 4 or Parallelism value if set
	workerCount := 4
	if config.Parallelism > 0 {
		workerCount = config.Parallelism
	}

	// Limit workers to number of chapters
	if workerCount > len(chapterKeys) {
		workerCount = len(chapterKeys)
	}

	// Create channel to distribute work
	chapterChan := make(chan struct {
		idx        int
		chapterNum float64
	}, len(chapterKeys))

	// Start worker goroutines
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for work := range chapterChan {
				idx := work.idx
				chapterNum := work.chapterNum
				files := chapterFiles[chapterNum]

				if logger != nil {
					logger.Info(fmt.Sprintf("Processing chapter %g from volume %d (%d/%d) - %d files",
						chapterNum, volNum, idx+1, len(chapterFiles), len(files)))
				}

				// Create temporary directory for this chapter
				tempDir, err := os.MkdirTemp("", "manga-chapter-*")
				if err != nil {
					if logger != nil {
						logger.Error(fmt.Sprintf("Error creating temp directory: %v", err))
					}
					continue
				}

				// Extract image files for this chapter directly to the temp directory
				imagePaths := make([]string, 0, len(files))

				// Use buffer for efficient I/O
				buffer := make([]byte, 4*1024*1024) // 4MB buffer

				for fileIdx, filePath := range files {
					// Look up file in zip index
					file, exists := zipIndex[filePath]
					if !exists {
						if logger != nil {
							logger.Warning(fmt.Sprintf("File not found in zip: %s", filePath))
						}
						continue
					}

					// Preserve just the basename for flatter extraction
					origFilename := filepath.Base(filePath)
					outPath := filepath.Join(tempDir, origFilename)

					// Check for duplicates and rename if needed
					if _, err := os.Stat(outPath); err == nil {
						ext := filepath.Ext(outPath)
						base := strings.TrimSuffix(outPath, ext)
						outPath = fmt.Sprintf("%s_%d%s", base, fileIdx, ext)
					}

					if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
						if logger != nil {
							logger.Error(fmt.Sprintf("Error creating directory: %v", err))
						}
						continue
					}

					outFile, err := os.Create(outPath)
					if err != nil {
						if logger != nil {
							logger.Error(fmt.Sprintf("Error creating file: %v", err))
						}
						continue
					}

					rc, err := file.Open()
					if err != nil {
						outFile.Close()
						if logger != nil {
							logger.Error(fmt.Sprintf("Error opening file in zip: %v", err))
						}
						continue
					}

					// Use buffered copying
					_, err = io.CopyBuffer(outFile, rc, buffer)
					outFile.Close()
					rc.Close()

					if err != nil {
						if logger != nil {
							logger.Error(fmt.Sprintf("Error copying file: %v", err))
						}
						continue
					}

					imagePaths = append(imagePaths, outPath)
				}

				if len(imagePaths) == 0 {
					if logger != nil {
						logger.Error(fmt.Sprintf("No valid images found for chapter %g", chapterNum))
					}
					os.RemoveAll(tempDir)
					continue
				}

				// Sort image paths (helps ensure proper page order)
				// Pre-compute page numbers for all files to avoid repeated regex operations
				type fileInfo struct {
					path     string
					pageNum  int
					filename string
				}

				fileInfos := make([]fileInfo, len(imagePaths))

				// Compile regex patterns once outside the loop
				pagePatternRegex := regexp.MustCompile(`\s+-\s+p(\d+(?:-p\d+)?)`)
				numPatternRegex := regexp.MustCompile(`^(\d+)`)

				for i, path := range imagePaths {
					filename := filepath.Base(path)
					pageNum := 0

					// Look for "pXXX" pattern in filename
					if match := pagePatternRegex.FindStringSubmatch(filename); len(match) > 1 {
						// For spread pages like "p006-p007", just use the first number
						pageStr := strings.Split(match[1], "-")[0]
						if num, err := strconv.Atoi(pageStr); err == nil {
							pageNum = num
						}
					} else {
						// Try to extract numbers like 001.jpg, 002.jpg
						if match := numPatternRegex.FindStringSubmatch(filename); len(match) > 1 {
							if num, err := strconv.Atoi(match[1]); err == nil {
								pageNum = num
							}
						}
					}

					fileInfos[i] = fileInfo{
						path:     path,
						pageNum:  pageNum,
						filename: filename,
					}
				}

				// Sort using pre-computed page numbers
				sort.Slice(fileInfos, func(i, j int) bool {
					if fileInfos[i].pageNum != 0 && fileInfos[j].pageNum != 0 {
						return fileInfos[i].pageNum < fileInfos[j].pageNum
					}
					// Fallback to filename comparison if page numbers are not found
					return fileInfos[i].filename < fileInfos[j].filename
				})

				// Update imagePaths with sorted paths
				for i, info := range fileInfos {
					imagePaths[i] = info.path
				}

				// Clean series name for filesystem
				// cleanSeries := sanitizeForFilesystem(seriesName)

				// Use volume number as chapter number if chapter_num seems invalid
				if volNum > 0 && chapterNum < 0 {
					if logger != nil {
						logger.Warning(fmt.Sprintf("Invalid chapter number %g detected, using volume number %d instead", chapterNum, volNum))
					}
					chapterNum = float64(volNum)
				}

				// Get chapter title
				var chapterTitle string
				if config != nil && config.ChapterTitles != nil {
					// Try from the config
					if title, exists := config.ChapterTitles[chapterNum]; exists && title != "" {
						chapterTitle = title
					} else {
						chapterTitle = fmt.Sprintf("Chapter %g", chapterNum)
					}
				} else {
					chapterTitle = fmt.Sprintf("Chapter %g", chapterNum)
				}

				// Create chapter filename - simple format without volume numbers
				// Format: {series title} - {chapter number}.cbz
				// Use 4-digit zero-padded integer, with decimal part if present (e.g., 0058.1.cbz, 0100.15.cbz)
				var chapterFilename string
				intPart := int(chapterNum)
				fracPart := chapterNum - float64(intPart)
				if fracPart > 0.001 { // Use small epsilon to avoid floating point errors
					// Has fractional part - format the full number then extract decimal part
					fullStr := fmt.Sprintf("%.10f", chapterNum) // Use enough precision
					// Remove trailing zeros and find decimal point
					fullStr = strings.TrimRight(fullStr, "0")
					fullStr = strings.TrimRight(fullStr, ".")
					// Extract just the decimal part after the dot
					parts := strings.Split(fullStr, ".")
					if len(parts) == 2 {
						chapterFilename = fmt.Sprintf("V%03d Ch%04d %s.%s.cbz", volNum, intPart, chapterTitle, parts[1])
					} else {
						// Shouldn't happen, but fallback
						chapterFilename = fmt.Sprintf("V%03d Ch%04d %s.cbz", volNum, intPart, chapterTitle)
					}
				} else {
					// No fractional part (e.g., 58 -> "0058.cbz")
					chapterFilename = fmt.Sprintf("V%03d Ch%04d %s.cbz", volNum, intPart, chapterTitle)
				}

				destPath := filepath.Join(outputDir, chapterFilename) // Check for double-page spread
				doublePages := false
				for _, path := range imagePaths {
					if doublePageRegex.MatchString(filepath.Base(path)) {
						doublePages = true
						break
					}
				}

				// Extract volume number from image filenames (overrides parent ZIP volume number)
				// This handles cases where the ZIP filename doesn't have proper volume info
				// but the individual images do (e.g., "One Piece - c1000 (v099) - p001.png")
				extractedVolNum := 0
				for _, imgPath := range imagePaths {
					imgBaseName := filepath.Base(imgPath)
					imgVolNum := extractVolumeNumber(imgBaseName)
					if imgVolNum > 0 {
						extractedVolNum = imgVolNum
						break // Use the first valid volume number found
					}
				}
				// Override parent volume number if we found one in the image filenames
				if extractedVolNum > 0 {
					if extractedVolNum != volNum && logger != nil {
						logger.Info(fmt.Sprintf("Chapter %g: Using volume number %d from image filenames (parent ZIP indicated volume %d)", chapterNum, extractedVolNum, volNum))
					}
					volNum = extractedVolNum
				}

				// Create metadata
				comicInfo, err := createComicInfoXML("chapter", chapterNum, seriesName, volNum, doublePages, chapterTitle, config)
				if err != nil {
					if logger != nil {
						logger.Error(fmt.Sprintf("Error creating ComicInfo.xml: %v", err))
					}
					os.RemoveAll(tempDir)
					continue
				}

				// Write ComicInfo.xml to temp directory
				comicInfoPath := filepath.Join(tempDir, "ComicInfo.xml")
				if err := os.WriteFile(comicInfoPath, comicInfo, 0644); err != nil {
					if logger != nil {
						logger.Error(fmt.Sprintf("Error writing ComicInfo.xml: %v", err))
					}
					os.RemoveAll(tempDir)
					continue
				}

				// Create CBZ file
				if err := createCBZ(tempDir, destPath); err != nil {
					if logger != nil {
						logger.Error(fmt.Sprintf("Error creating chapter CBZ: %v", err))
					}
					os.RemoveAll(tempDir)
					continue
				}

				mu.Lock()
				successCount++
				currentProgress := successCount
				mu.Unlock()

				if logger != nil {
					logger.Info(fmt.Sprintf("Created chapter %04g (%d pages) with title: %s", chapterNum, len(imagePaths), chapterTitle))
				}

				os.RemoveAll(tempDir)

				// Update processing status
				if config.Process != nil {
					updateProcessStatus(config.Process, currentProgress, config.Process.Total, fmt.Sprintf("Completed %d/%d", currentProgress, config.Process.Total))
				}
			}
		}()
	}

	// Send work to workers
	for idx, chapterNum := range chapterKeys {
		chapterChan <- struct {
			idx        int
			chapterNum float64
		}{idx, chapterNum}
	}
	close(chapterChan)

	// Wait for all workers to finish
	wg.Wait()

	// Calculate and log elapsed time
	elapsedTime := time.Since(startTime).Round(time.Millisecond)
	if logger != nil {
		logger.Info(fmt.Sprintf("Volume processing completed in %v", elapsedTime))
	}

	if successCount > 0 {
		if logger != nil {
			logger.Info(fmt.Sprintf("Successfully extracted %d chapters from volume %d", successCount, volNum))

			// Output final filename (for reference in logs)
			volumeFilename := fmt.Sprintf("V%03d", volNum)
			logger.Info(fmt.Sprintf("Processed %s into individual chapter files (volume %s)",
				baseName, volumeFilename))
		}

		// Delete original if requested and we successfully extracted chapters
		if config.DeleteOriginals {
			if err := os.Remove(filePath); err != nil {
				if logger != nil {
					logger.Warning(fmt.Sprintf("Error deleting original volume: %v", err))
				}
			} else if logger != nil {
				logger.Info(fmt.Sprintf("Deleted original volume: %s", baseName))
			}
		}
		return nil
	}

	return fmt.Errorf("failed to extract any chapters from volume %s", baseName)
}

// ProcessCBZFile processes a CBZ file (volume or chapter)
func ProcessCBZFile(filePath, fileType, seriesName string, volumeNumber int, outputDir string, config *Config) error {
	logger := config.Logger
	baseName := filepath.Base(filePath)
	startTime := time.Now()

	// Reduced logging: commenting out verbose start message
	// if logger != nil {
	// 	logger.Info(fmt.Sprintf("Starting to process %s file: %s", fileType, baseName))
	// }

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Processing %s file: %s", fileType, baseName))
	}

	// Extract chapter number if it's a chapter
	chapterNum := float64(0)
	if fileType == "chapter" {
		if config.IsOneshot {
			// For oneshots, always force chapter number to 1
			chapterNum = 1.0
		} else {
			chapterNum = extractChapterNumber(baseName)
			if chapterNum == 0 {
				if logger != nil {
					logger.Warning(fmt.Sprintf("Could not extract chapter number from %s, using 0", baseName))
				}
			}
		}
	}

	// Clean series name for filesystem
	cleanSeries := sanitizeForFilesystem(seriesName)

	// Determine chapter title early if available in config (will be used for output filename)
	var chapterTitle string
	if fileType == "chapter" && chapterNum >= 0 {
		// Try to get chapter title from config
		if config != nil && config.ChapterTitles != nil {
			if title, exists := config.ChapterTitles[chapterNum]; exists && title != "" {
				chapterTitle = title
				// Reduced logging: commenting out to reduce verbosity
				// if logger != nil {
				// 	logger.Info(fmt.Sprintf("Using title for chapter %g from config: %s", chapterNum, title))
				// }
			}
		}
	}

	// Create output filename
	var outputFilename string
	if fileType == "chapter" {
		// Handle oneshot special case - use simple filename
		if config.IsOneshot {
			// For oneshots, use simple format: just the series name
			outputFilename = fmt.Sprintf("%s.cbz", cleanSeries)
		} else {
			// Format: {series title} - {chapter number}.cbz
			// Use 4-digit zero-padded integer, with decimal part if present (e.g., 0058.1.cbz, 0100.15.cbz)
			intPart := int(chapterNum)
			fracPart := chapterNum - float64(intPart)
			if fracPart > 0.001 { // Use small epsilon to avoid floating point errors
				// Has fractional part - format the full number then extract decimal part
				fullStr := fmt.Sprintf("%.10f", chapterNum) // Use enough precision
				// Remove trailing zeros and find decimal point
				fullStr = strings.TrimRight(fullStr, "0")
				fullStr = strings.TrimRight(fullStr, ".")
				// Extract just the decimal part after the dot
				parts := strings.Split(fullStr, ".")
				if len(parts) == 2 {
					outputFilename = fmt.Sprintf("%s - %04d.%s.cbz", cleanSeries, intPart, parts[1])
				} else {
					// Shouldn't happen, but fallback
					outputFilename = fmt.Sprintf("%s - %04d.cbz", cleanSeries, intPart)
				}
			} else {
				// No fractional part (e.g., 58 -> "0058.cbz")
				outputFilename = fmt.Sprintf("%s - %04d.cbz", cleanSeries, intPart)
			}
		}
	} else {
		// Format: {series title} - Vol {volume number with 4 digits}.cbz
		outputFilename = fmt.Sprintf("%s - Vol %04d.cbz", cleanSeries, volumeNumber)
	}

	// Create output path
	outputPath := filepath.Join(outputDir, outputFilename)

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("error creating output directory: %v", err)
	}

	// Fast path for single volume processing
	if fileType == "volume" {
		if logger != nil {
			logger.Info(fmt.Sprintf("Using optimized processing for volume %d", volumeNumber))
		}

		// Create temp directory just for ComicInfo.xml
		tempDir, err := os.MkdirTemp("", "manga-processor-*")
		if err != nil {
			return fmt.Errorf("error creating temp directory: %v", err)
		}
		defer os.RemoveAll(tempDir)

		// For volumes we don't need to analyze page by page, just use default values
		doublePages := false
		chapterTitle := fmt.Sprintf("Volume %d", volumeNumber)

		// Create metadata
		comicInfo, err := createComicInfoXML(fileType, chapterNum, seriesName, volumeNumber, doublePages, chapterTitle, config)
		if err != nil {
			return fmt.Errorf("error creating ComicInfo.xml: %v", err)
		}

		// Quick check if the file already has ComicInfo.xml
		zipReader, err := zip.OpenReader(filePath)
		if err != nil {
			return fmt.Errorf("error opening zip file: %v", err)
		}

		hasComicInfo := false
		for _, f := range zipReader.File {
			if f.Name == "ComicInfo.xml" {
				hasComicInfo = true
				break
			}
		}
		zipReader.Close()

		// If the file does not have ComicInfo.xml, we can simply copy it and add the XML
		if !hasComicInfo {
			// Create a new temp directory for adding ComicInfo.xml to the existing ZIP
			addXmlTempDir, err := os.MkdirTemp("", "manga-add-xml-*")
			if err != nil {
				return fmt.Errorf("error creating temp directory: %v", err)
			}
			defer os.RemoveAll(addXmlTempDir)

			// Write ComicInfo.xml to temp directory
			comicInfoPath := filepath.Join(addXmlTempDir, "ComicInfo.xml")
			if err := os.WriteFile(comicInfoPath, comicInfo, 0644); err != nil {
				return fmt.Errorf("error writing ComicInfo.xml: %v", err)
			}

			// Create a temporary file for the modified ZIP
			outputTempFile, err := os.CreateTemp("", "manga-mod-*.zip")
			if err != nil {
				return fmt.Errorf("error creating temp file: %v", err)
			}
			outputTempFile.Close() // Close it so we can use it as a path
			outputTempPath := outputTempFile.Name()
			defer os.Remove(outputTempPath) // Clean up when done

			// Open the original ZIP for reading
			r, err := zip.OpenReader(filePath)
			if err != nil {
				return fmt.Errorf("error opening original ZIP: %v", err)
			}
			defer r.Close()

			// Create the new ZIP file
			w, err := os.Create(outputTempPath)
			if err != nil {
				return fmt.Errorf("error creating new ZIP: %v", err)
			}
			defer w.Close()

			// Create a ZIP writer
			zw := zip.NewWriter(w)
			defer zw.Close()

			// First, add ComicInfo.xml with Store method (no compression)
			comicInfoContent, err := os.ReadFile(comicInfoPath)
			if err != nil {
				return fmt.Errorf("error reading ComicInfo.xml: %v", err)
			}

			comicInfoHeader := &zip.FileHeader{
				Name:     "ComicInfo.xml",
				Method:   zip.Store, // Use Store mode for consistency and speed
				Modified: time.Now(),
			}

			writer, err := zw.CreateHeader(comicInfoHeader)
			if err != nil {
				return fmt.Errorf("error creating ZIP entry for ComicInfo.xml: %v", err)
			}
			if _, err := writer.Write(comicInfoContent); err != nil {
				return fmt.Errorf("error writing ComicInfo.xml to ZIP: %v", err)
			}

			// Then, copy all other files from the original ZIP
			for _, f := range r.File {
				// Skip any existing ComicInfo.xml
				if f.Name == "ComicInfo.xml" {
					continue
				}

				// Copy the file header
				header, err := zip.FileInfoHeader(f.FileInfo())
				if err != nil {
					return fmt.Errorf("error creating file header: %v", err)
				}
				header.Name = f.Name
				// Force Store mode for all files to ensure CBZ is uncompressed
				header.Method = zip.Store

				// Create the file entry
				writer, err := zw.CreateHeader(header)
				if err != nil {
					return fmt.Errorf("error creating ZIP entry: %v", err)
				}

				// Open the source file
				rc, err := f.Open()
				if err != nil {
					return fmt.Errorf("error opening file in source ZIP: %v", err)
				}

				// Copy the content
				if _, err := io.Copy(writer, rc); err != nil {
					rc.Close()
					return fmt.Errorf("error copying file content: %v", err)
				}
				rc.Close()
			}

			// Close the writers
			if err := zw.Close(); err != nil {
				return fmt.Errorf("error closing ZIP writer: %v", err)
			}
			if err := w.Close(); err != nil {
				return fmt.Errorf("error closing file: %v", err)
			}

			// Create output directory if it doesn't exist
			if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
				return fmt.Errorf("error creating output directory: %v", err)
			}

			// Move the modified ZIP to the final location
			if err := os.Rename(outputTempPath, outputPath); err != nil {
				// If rename fails (e.g., cross-device), try to copy the file
				if copyErr := copyFile(outputTempPath, outputPath); copyErr != nil {
					return fmt.Errorf("error moving modified ZIP to final location: %v (copy error: %v)", err, copyErr)
				}
				// Remove the temp file after successful copy
				os.Remove(outputTempPath)
			}

			if logger != nil {
				logger.Info(fmt.Sprintf("Successfully processed %s and added ComicInfo.xml", baseName))
				logger.Info(fmt.Sprintf("Created: %s", outputFilename))
			}

			// Delete original if requested
			if config.DeleteOriginals {
				if err := os.Remove(filePath); err != nil {
					if logger != nil {
						logger.Warning(fmt.Sprintf("Error deleting original file: %v", err))
					}
				} else if logger != nil {
					logger.Info(fmt.Sprintf("Deleted original file: %s", baseName))
				}
			}

			return nil
		}

		// Otherwise, we continue with standard processing
		if logger != nil {
			logger.Info("Input file has existing ComicInfo.xml, using standard processing path")
		}
	}

	// Standard processing path
	// Create temp directory
	tempDir, err := os.MkdirTemp("", "manga-processor-*")
	if err != nil {
		return fmt.Errorf("error creating temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Reduced logging: only update process status, not info log
	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Extracting %s file: %s", fileType, baseName))
	}

	// Extract the ZIP file
	imagePaths := []string{}
	if err := extractZip(filePath, tempDir); err != nil {
		return fmt.Errorf("extraction failed for %s: %v", baseName, err)
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Finding image files in %s", baseName))
	}

	// Find all image files and check for double pages in a single pass
	// chapterTitle is already declared earlier in this function
	doublePages := false

	// Pre-compile regex once
	doublePageRegexCompiled := doublePageRegex

	// Use a more efficient approach with WalkDir instead of Walk
	err = filepath.WalkDir(tempDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip directories themselves in this check
		if d.IsDir() {
			if path != tempDir {
				// Extract chapter title from directory name
				dirName := filepath.Base(path)
				title := extractChapterTitleFromDir(dirName)
				if title != "" && fileType == "chapter" {
					chapterTitle = title
					if logger != nil {
						logger.Info(fmt.Sprintf("Found chapter title from directory: %s", chapterTitle))
					}
				}
			}
			return nil
		}

		// Process files
		fileName := d.Name()

		// Skip ComicInfo.xml and hidden files
		if fileName == "ComicInfo.xml" || strings.HasPrefix(fileName, ".") {
			return nil
		}

		if isImageFile(fileName) {
			imagePaths = append(imagePaths, path)

			// Check for double-page spread in the same pass
			if !doublePages && doublePageRegexCompiled.MatchString(fileName) {
				doublePages = true
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("error scanning extracted files: %v", err)
	}

	if len(imagePaths) == 0 {
		return fmt.Errorf("no valid images found in %s", baseName)
	}

	// Sort image paths (helps ensure proper page order)
	// Reduced logging: only update process status, no info log
	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Sorting %d images from %s", len(imagePaths), baseName))
	}

	// Pre-compute page numbers for all files to avoid repeated regex operations
	type fileInfo struct {
		path     string
		pageNum  int
		filename string
	}

	// Pre-compile regex patterns outside the loop
	pagePatternRegex := regexp.MustCompile(`\s+-\s+p(\d+(?:-p\d+)?)`)
	numPatternRegex := regexp.MustCompile(`^(\d+)`)

	fileInfos := make([]fileInfo, len(imagePaths))

	// Use parallel processing for page number extraction
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Calculate number of workers - use up to CPU count or 4, whichever is less
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}

	// Don't use more workers than files
	if workers > len(imagePaths) {
		workers = len(imagePaths)
	}

	// Create work batches
	batchSize := (len(imagePaths) + workers - 1) / workers

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Calculate this worker's range
			start := workerID * batchSize
			end := start + batchSize
			if end > len(imagePaths) {
				end = len(imagePaths)
			}

			// Process assigned files
			for i := start; i < end; i++ {
				path := imagePaths[i]
				filename := filepath.Base(path)
				pageNum := 0

				// Look for "pXXX" pattern in filename
				if match := pagePatternRegex.FindStringSubmatch(filename); len(match) > 1 {
					// For spread pages like "p006-p007", just use the first number
					pageStr := strings.Split(match[1], "-")[0]
					if num, err := strconv.Atoi(pageStr); err == nil {
						pageNum = num
					}
				} else {
					// Try to extract numbers like 001.jpg, 002.jpg
					if match := numPatternRegex.FindStringSubmatch(filename); len(match) > 1 {
						if num, err := strconv.Atoi(match[1]); err == nil {
							pageNum = num
						}
					}
				}

				// Store results
				mu.Lock()
				fileInfos[i] = fileInfo{
					path:     path,
					pageNum:  pageNum,
					filename: filename,
				}
				mu.Unlock()
			}
		}(w)
	}

	// Wait for all workers to finish
	wg.Wait()

	// Sort using pre-computed page numbers
	sort.Slice(fileInfos, func(i, j int) bool {
		if fileInfos[i].pageNum != 0 && fileInfos[j].pageNum != 0 {
			return fileInfos[i].pageNum < fileInfos[j].pageNum
		}
		// Fallback to filename comparison if page numbers are not found
		return fileInfos[i].filename < fileInfos[j].filename
	})

	// Update imagePaths with sorted paths
	for i, info := range fileInfos {
		imagePaths[i] = info.path
	}

	// Print the first few sorted paths for debugging (reduced logging)
	// Only log this in debug scenarios, not for every chapter
	// Commented out to reduce log verbosity
	/*
		if logger != nil && len(imagePaths) > 0 {
			logger.Info("First 3 pages after sorting:")
			for i := 0; i < min(3, len(imagePaths)); i++ {
				logger.Info(fmt.Sprintf("  %s", filepath.Base(imagePaths[i])))
			}

			if len(imagePaths) > 3 {
				logger.Info("Last 3 pages after sorting:")
				for i := max(3, len(imagePaths)-3); i < len(imagePaths); i++ {
					logger.Info(fmt.Sprintf("  %s", filepath.Base(imagePaths[i])))
				}
			}
		}
	*/

	// Determine chapter title if not already found from directory
	if fileType == "chapter" && chapterNum >= 0 && chapterTitle == "" {
		// Try to get chapter title from config
		if title, exists := config.ChapterTitles[chapterNum]; exists && title != "" {
			chapterTitle = title
			// Reduced logging: only log if no title was found earlier
			// Commenting out to reduce verbosity
			// if logger != nil {
			// 	logger.Info(fmt.Sprintf("Using title for chapter %g from config: %s", chapterNum, title))
			// }
		} else {
			chapterTitle = fmt.Sprintf("Chapter %g", chapterNum)
			if logger != nil {
				logger.Warning(fmt.Sprintf("No title found for chapter %g, using default: %s", chapterNum, chapterTitle))
			}
		}
	}

	// Extract volume number from image filenames if not already provided
	// This handles cases where the CBZ filename doesn't have proper volume info
	// but the individual images do (e.g., "One Piece - c0600 (v061) - p115.jpg")
	if volumeNumber == 0 && fileType == "chapter" {
		for _, imgPath := range imagePaths {
			imgBaseName := filepath.Base(imgPath)
			imgVolNum := extractVolumeNumber(imgBaseName)
			if imgVolNum > 0 {
				volumeNumber = imgVolNum
				// Reduced logging: commenting out to reduce verbosity
				// if logger != nil {
				// 	logger.Info(fmt.Sprintf("Chapter %g: Using volume number %d from image filename: %s", chapterNum, volumeNumber, imgBaseName))
				// }
				break // Use the first valid volume number found
			}
		}
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Creating metadata for %s", baseName))
	}

	// Create ComicInfo.xml
	comicInfo, err := createComicInfoXML(fileType, chapterNum, seriesName, volumeNumber, doublePages, chapterTitle, config)
	if err != nil {
		return fmt.Errorf("error creating ComicInfo.xml: %v", err)
	}

	// Write ComicInfo.xml to temp directory
	comicInfoPath := filepath.Join(tempDir, "ComicInfo.xml")
	if err := os.WriteFile(comicInfoPath, comicInfo, 0644); err != nil {
		return fmt.Errorf("error writing ComicInfo.xml: %v", err)
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Creating CBZ file: %s", outputFilename))
	}

	// Create CBZ file directly in the destination
	if err := createCBZ(tempDir, outputPath); err != nil {
		return fmt.Errorf("error creating CBZ: %v", err)
	}

	// Calculate and log elapsed time
	elapsedTime := time.Since(startTime).Round(time.Millisecond)
	if logger != nil {
		logger.Info(fmt.Sprintf("Processing completed in %v", elapsedTime))

		if fileType == "chapter" {
			logger.Info(fmt.Sprintf("Processed %s into %s", baseName, outputFilename))
		} else {
			logger.Info(fmt.Sprintf("Processed %s into %s", baseName, outputFilename))
		}
	}

	if config.Process != nil {
		updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Successfully created: %s", outputFilename))
	}

	// Delete original if requested
	if config.DeleteOriginals {
		if err := os.Remove(filePath); err != nil {
			if logger != nil {
				logger.Warning(fmt.Sprintf("Error deleting original file: %v", err))
			}
		} else if logger != nil {
			logger.Info(fmt.Sprintf("Deleted original file: %s", baseName))
		}
	}

	return nil
}

// ProcessBatch processes a batch of files
func ProcessBatch(files []string, seriesName, outputDir string, config *Config) error {
	if len(files) == 0 {
		return nil
	}

	logger := config.Logger
	if logger == nil {
		return fmt.Errorf("logger is required")
	}

	// Initial log - reduced verbosity
	logger.Info(fmt.Sprintf("Starting batch processing of %d files for %s", len(files), seriesName))

	// Separate volumes and chapters
	volumes := []string{}
	chapters := []string{}

	for _, file := range files {
		baseName := filepath.Base(file)

		// For oneshots, force all files to be treated as chapters
		if config.IsOneshot {
			chapters = append(chapters, file)
			continue
		}

		volNum := extractVolumeNumber(baseName)
		chapterNum := extractChapterNumber(baseName)

		// Priority: If we found a chapter number (including Chapter 0), treat it as a chapter
		if chapterNum >= 0 && (chapterRegex.MatchString(baseName) || chapterNum > 0) {
			chapters = append(chapters, file)
			// Removed excessive "Identified chapter file" logging
		} else if volumeRegex.MatchString(baseName) || volNum > 0 {
			// If no chapter number but has volume indicator, treat as volume
			if volNum == 0 {
				volNum = 1
			}
			volumes = append(volumes, file)
			// Removed excessive "Identified volume file" logging
		} else {
			// If no chapter or volume number found, treat as volume 1
			volumes = append(volumes, file)
			// Removed excessive "Identified volume file" logging
		}
	}

	// Log summary only
	logger.Info(fmt.Sprintf("Processing %d volumes and %d chapters", len(volumes), len(chapters)))

	// Update total count in status
	if config.Process != nil {
		updateProcessStatus(config.Process, 0, len(volumes)+len(chapters), fmt.Sprintf("Starting process: %d volumes, %d chapters", len(volumes), len(chapters)))
	}

	// Calculate dynamic concurrency based on CPU count
	parallelism := config.Parallelism
	if parallelism <= 0 {
		// Use fixed parallelism of 4 workers
		parallelism = 4
		logger.Info(fmt.Sprintf("Using default parallelism: %d workers", parallelism))
	}

	// Shared semaphore for limiting concurrency across both volumes and chapters
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup

	// Shared counters for progress
	var (
		mu             sync.Mutex
		processedCount int
		errorCount     int
	)

	// Process volumes in parallel with concurrency control
	if len(volumes) > 0 {
		logger.Info(fmt.Sprintf("Processing %d volumes with parallelism %d", len(volumes), parallelism))

		for i, file := range volumes {
			wg.Add(1)
			sem <- struct{}{} // Acquire token

			go func(idx int, filePath string) {
				defer wg.Done()
				defer func() { <-sem }() // Release token

				baseName := filepath.Base(filePath)
				volumeNum := extractVolumeNumber(baseName)

				// Update status
				mu.Lock()
				statusMsg := fmt.Sprintf("Processing volume %d (%d/%d)", volumeNum, idx+1, len(volumes))
				mu.Unlock()

				if config.Process != nil {
					updateProcessStatus(config.Process, 0, 0, statusMsg)
				}

				logger.Info(fmt.Sprintf("Processing volume %d/%d: %s", idx+1, len(volumes), baseName))

				// Process volume
				startTime := time.Now()
				if err := ProcessVolumeFile(filePath, outputDir, seriesName, config); err != nil {
					mu.Lock()
					errorCount++
					mu.Unlock()
					logger.Error(fmt.Sprintf("Error processing volume file %s: %v", baseName, err))
					return
				}

				duration := time.Since(startTime).Round(time.Second)
				logger.Info(fmt.Sprintf("Completed volume %d/%d: %s in %v", idx+1, len(volumes), baseName, duration))

				// Update progress
				mu.Lock()
				processedCount++
				progress := processedCount
				mu.Unlock()

				// Update status
				if config.Process != nil {
					updateProcessStatus(config.Process, progress, config.Process.Total, fmt.Sprintf("Completed volume %d/%d", progress, config.Process.Total))
				}
			}(i, file)
		}

		// Wait for volumes to complete
		wg.Wait()
		logger.Info(fmt.Sprintf("Completed processing %d volumes (%d errors)", len(volumes), errorCount))

		// Reset counters for chapters
		mu.Lock()
		processedCount = 0
		errorCount = 0
		mu.Unlock()
	}

	// Process chapters in parallel
	if len(chapters) > 0 {
		logger.Info(fmt.Sprintf("Processing %d chapters with parallelism %d", len(chapters), parallelism))

		for i, file := range chapters {
			wg.Add(1)
			sem <- struct{}{} // Acquire token

			go func(idx int, filePath string) {
				defer wg.Done()
				defer func() { <-sem }() // Release token

				baseName := filepath.Base(filePath)
				chapterNum := extractChapterNumber(baseName)

				// Update status and log - combined into one line
				if config.Process != nil {
					updateProcessStatus(config.Process, 0, 0, fmt.Sprintf("Processing chapter %g (%d/%d)", chapterNum, idx+1, len(chapters)))
				}

				// Reduced logging: only log start, not separate "Processing" and "Completed" messages
				// logger.Info(fmt.Sprintf("Processing chapter %d/%d: %s", idx+1, len(chapters), baseName))

				// Process chapter
				startTime := time.Now()
				if err := ProcessCBZFile(filePath, "chapter", seriesName, 0, outputDir, config); err != nil {
					mu.Lock()
					errorCount++
					mu.Unlock()
					logger.Error(fmt.Sprintf("Error processing chapter file %s: %v", baseName, err))
					return
				}

				duration := time.Since(startTime).Round(time.Second)
				// Simplified: single line showing completion with timing
				logger.Info(fmt.Sprintf("Chapter %d/%d completed: %s (%v)", idx+1, len(chapters), baseName, duration))

				// Update progress
				mu.Lock()
				processedCount++
				progress := processedCount
				mu.Unlock()

				// Update status
				if config.Process != nil {
					updateProcessStatus(config.Process, len(volumes)+progress, config.Process.Total, fmt.Sprintf("Processed %d/%d chapters", progress, config.Process.Total))
				}
			}(i, file)
		}

		// Wait for chapters to complete
		wg.Wait()
		logger.Info(fmt.Sprintf("Completed processing %d chapters (%d errors)", len(chapters), errorCount))
	}

	// Final status update
	if config.Process != nil {
		updateProcessStatus(config.Process, config.Process.Total, config.Process.Total, "Processing complete")
	}

	return nil
}

// helper function to get minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BatchLogManager handles summarized logging for large batch operations
type BatchLogManager struct {
	logger          Logger
	operationName   string
	completedCount  int
	totalCount      int
	lastReportTime  time.Time
	reportThreshold int           // Report after this many items
	reportInterval  time.Duration // Report at least this often
	mutex           sync.Mutex
}

// NewBatchLogManager creates a new batch log manager
func NewBatchLogManager(logger Logger, operationName string, totalCount int) *BatchLogManager {
	return &BatchLogManager{
		logger:          logger,
		operationName:   operationName,
		completedCount:  0,
		totalCount:      totalCount,
		lastReportTime:  time.Now(),
		reportThreshold: 5,                // Report every 5 items
		reportInterval:  10 * time.Second, // Report at least every 10 seconds
	}
}

// LogItemCompleted logs a completed item and periodically reports progress
func (b *BatchLogManager) LogItemCompleted(itemName string) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.completedCount++

	// Determine if we should log now
	shouldReport := false

	// Report if we've hit the threshold or it's been a while since last report
	if b.completedCount%b.reportThreshold == 0 ||
		time.Since(b.lastReportTime) > b.reportInterval {
		shouldReport = true
	}

	// Always report the first and last items
	if b.completedCount == 1 || b.completedCount == b.totalCount {
		shouldReport = true
	}

	if shouldReport {
		percentComplete := float64(b.completedCount) / float64(b.totalCount) * 100
		if b.totalCount > 10 {
			// For large batches, provide summarized progress reports
			b.logger.Info(fmt.Sprintf("▶ %s progress: %d/%d (%.1f%%) completed - latest: %s",
				b.operationName, b.completedCount, b.totalCount, percentComplete, itemName))
		} else {
			// For smaller batches, provide more detail
			b.logger.Info(fmt.Sprintf("✓ %s: %s (%d of %d)",
				b.operationName, itemName, b.completedCount, b.totalCount))
		}
		b.lastReportTime = time.Now()
	}
}

// ReportCompletion logs the final completion message
func (b *BatchLogManager) ReportCompletion() {
	b.logger.Info(fmt.Sprintf("✓ %s complete! Successfully processed %d items",
		b.operationName, b.completedCount))
}

// extractChapterTitleFromDir extracts chapter titles from directory names
func extractChapterTitleFromDir(dirName string) string {
	if dirName == "" || dirName == "." {
		return ""
	}

	// Try different patterns for chapter title extraction

	// Common pattern: "Chapter X - Title" or any variation with chapter number and title
	simpleRegex := regexp.MustCompile(`(?i)Chapter\s+\d+\s*[-:]\s*(.+)$`)
	if match := simpleRegex.FindStringSubmatch(dirName); len(match) > 1 && match[1] != "" {
		return strings.TrimSpace(match[1])
	}

	// Alternative pattern with letters instead of "Chapter", like "Ch 1 - Title"
	altRegex := regexp.MustCompile(`(?i)(?:Ch|Chapter)\.?\s+\d+\s*[-:]\s*(.+)$`)
	if match := altRegex.FindStringSubmatch(dirName); len(match) > 1 && match[1] != "" {
		return strings.TrimSpace(match[1])
	}

	// Most generic pattern: Just grab anything after number and dash/colon
	genericRegex := regexp.MustCompile(`\d+\s*[-:]\s*(.+)$`)
	if match := genericRegex.FindStringSubmatch(dirName); len(match) > 1 && match[1] != "" {
		return strings.TrimSpace(match[1])
	}

	// Last resort: Just use the whole folder name if it has "chapter" in it
	if strings.Contains(strings.ToLower(dirName), "chapter") {
		return dirName
	}

	return ""
}

// updateProcessStatus updates the process status using the proper methods
func updateProcessStatus(process *internal.Process, progress, total int, message string) {
	if process != nil {
		process.Update(progress, total, message)
	}
}

// groupFilesByChapter groups image files by chapter markers in their filenames
func groupFilesByChapter(files []string, logger Logger) map[float64][]string {
	result := make(map[float64][]string)

	if logger != nil {
		logger.Info(fmt.Sprintf("Analyzing %d files to identify chapters", len(files)))
	}

	// First pass: Look for explicit chapter markers in filenames
	for _, file := range files {
		baseName := filepath.Base(file)

		// Check for explicit chapter markers like "c123" in filename
		matches := chapterRegex.FindStringSubmatch(baseName)
		if len(matches) > 1 {
			chNum, err := strconv.ParseFloat(matches[1], 64)
			if err == nil {
				result[chNum] = append(result[chNum], file)
			}
		}
	}

	// If no explicit markers found, try to infer from directory structure
	if len(result) == 0 {
		if logger != nil {
			logger.Info("No chapter markers found in filenames, trying to infer from structure")
		}

		// Group by directories
		dirGroups := make(map[string][]string)
		for _, file := range files {
			dir := filepath.Dir(file)
			dirGroups[dir] = append(dirGroups[dir], file)
		}

		// Try to extract chapter numbers from directory names
		for dir, dirFiles := range dirGroups {
			baseDirName := filepath.Base(dir)

			// Look for chapter markers in directory name
			matches := chapterRegex.FindStringSubmatch(baseDirName)
			if len(matches) > 1 {
				chNum, err := strconv.ParseFloat(matches[1], 64)
				if err == nil {
					result[chNum] = append(result[chNum], dirFiles...)
					if logger != nil {
						logger.Info(fmt.Sprintf("Found chapter %g in directory: %s", chNum, baseDirName))
					}
				}
			} else {
				// Check for chapter folder patterns like "Chapter 001 - Title"
				folderChapterRegex := regexp.MustCompile(`(?i)(?:chapter|ch|c)[.\s_-]*(\d+(?:\.\d+)?)(?:\s|-|$)`)
				matches = folderChapterRegex.FindStringSubmatch(baseDirName)
				if len(matches) > 1 {
					chNum, err := strconv.ParseFloat(matches[1], 64)
					if err == nil {
						result[chNum] = append(result[chNum], dirFiles...)
						if logger != nil {
							logger.Info(fmt.Sprintf("Found chapter %g in directory with folder pattern: %s", chNum, baseDirName))
						}
					}
				}
			}

			// If we still haven't found chapters, check the full directory path
			// This handles nested folders like "Volume X/Chapter Y"
			if _, exists := result[0]; !exists && len(result) == 0 {
				dirParts := strings.Split(dir, string(filepath.Separator))
				for _, part := range dirParts {
					if part == "" {
						continue
					}

					matches = chapterRegex.FindStringSubmatch(part)
					if len(matches) > 1 {
						chNum, err := strconv.ParseFloat(matches[1], 64)
						if err == nil {
							result[chNum] = append(result[chNum], dirFiles...)
							if logger != nil {
								logger.Info(fmt.Sprintf("Found chapter %g in path component: %s", chNum, part))
							}
							break
						}
					}

					// Try folder chapter pattern
					folderChapterRegex := regexp.MustCompile(`(?i)(?:chapter|ch|c)[.\s_-]*(\d+(?:\.\d+)?)(?:\s|-|$)`)
					matches = folderChapterRegex.FindStringSubmatch(part)
					if len(matches) > 1 {
						chNum, err := strconv.ParseFloat(matches[1], 64)
						if err == nil {
							result[chNum] = append(result[chNum], dirFiles...)
							if logger != nil {
								logger.Info(fmt.Sprintf("Found chapter %g in path component with folder pattern: %s", chNum, part))
							}
							break
						}
					}
				}
			}
		}
	}

	// Check if we found any chapters
	if len(result) > 0 {
		if logger != nil {
			logger.Info(fmt.Sprintf("Successfully grouped %d files into %d chapters", len(files), len(result)))

			// Log the first few files of each chapter for verification
			chapters := make([]float64, 0, len(result))
			for ch := range result {
				chapters = append(chapters, ch)
			}
			sort.Float64s(chapters)

			for _, ch := range chapters[:min(3, len(chapters))] {
				chFiles := result[ch]
				if len(chFiles) > 0 {
					logger.Info(fmt.Sprintf("Chapter %g sample files:", ch))
					for i := 0; i < min(3, len(chFiles)); i++ {
						logger.Info(fmt.Sprintf("  - %s", filepath.Base(chFiles[i])))
					}
				}
			}
		}
	} else {
		if logger != nil {
			logger.Info("No chapters detected in files, will process as a single volume")
		}
	}

	return result
}

// Processor handles manga processing
type Processor struct {
	config *Config
}

// NewProcessor creates a new Processor
func NewProcessor(config *Config) *Processor {
	if config.Parallelism == 0 {
		config.Parallelism = 4
	}
	return &Processor{config: config}
}

// Analyzer handles manga analysis
type Analyzer struct {
	config *Config
}

// NewAnalyzer creates a new Analyzer
func NewAnalyzer(config *Config) *Analyzer {
	return &Analyzer{config: config}
}

// Process processes a manga
func (p *Processor) Process(manga *internal.Manga) (*internal.Process, error) {
	// This is a mock implementation for the debug script
	if p.config.Process != nil {
		p.config.Process.Update(100, 100, "Processing complete (mock)")
		return p.config.Process, nil
	}
	return nil, fmt.Errorf("no process configured")
}

// ScanDirectory scans a directory for manga series
func (a *Analyzer) ScanDirectory(dir string) ([]*internal.Series, error) {
	// This is a mock implementation for the debug script
	seriesMap := make(map[string]*internal.Series)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (strings.HasSuffix(info.Name(), ".cbz") || strings.HasSuffix(info.Name(), ".cbr") || strings.HasSuffix(info.Name(), ".zip")) {
			seriesName := filepath.Base(filepath.Dir(path))
			if _, ok := seriesMap[seriesName]; !ok {
				seriesMap[seriesName] = &internal.Series{Name: seriesName}
			}
			seriesMap[seriesName].Manga = append(seriesMap[seriesName].Manga, &internal.Manga{Name: info.Name(), Path: path})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var series []*internal.Series
	for _, s := range seriesMap {
		series = append(series, s)
	}
	return series, nil
}

// ProcessManga processes a manga series
func ProcessManga(series *internal.Series, config *Config) error {
	// This is a mock implementation for the debug script
	return nil
}
