package processor

import (
	"archive/zip"
	"fmt"
	"manga-tool/internal/util"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AnalyzeChaptersNeeded analyzes CBZ files to determine which chapters need titles and returns
// both the needed chapters and any titles discovered from directory names
func AnalyzeChaptersNeeded(cbzFiles []string, logger util.Logger) (map[float64]bool, map[float64]string) {
	neededChapters := make(map[float64]bool)
	// Store discovered chapter titles to add to config
	discoveredTitles := make(map[float64]string)

	// First pass: check if volumes contain chapters
	for _, filePath := range cbzFiles {
		baseName := filepath.Base(filePath)

		// Only check volumes
		if util.VolumePattern.MatchString(baseName) {
			reader, err := zip.OpenReader(filePath)
			if err != nil {
				logger.Warning(fmt.Sprintf("Skipping analysis of invalid CBZ file: %s", baseName))
				continue
			}
			defer reader.Close()

			// Track directories to detect folder structure
			directories := make(map[string]bool)
			chapterDirs := make(map[string]float64)
			chapterTitles := make(map[float64]string)

			// First, collect all directories and check for chapter markers
			for _, f := range reader.File {
				if f.FileInfo().IsDir() {
					directories[f.Name] = true

					// Try to extract chapter number and title from directory
					dirName := filepath.Base(f.Name)
					// Look for chapter number
					chMatches := util.ChapterPattern.FindStringSubmatch(dirName)
					if len(chMatches) > 1 {
						if chapterNum, err := strconv.ParseFloat(chMatches[1], 64); err == nil {
							// Store directory and chapter number
							chapterDirs[f.Name] = chapterNum
							neededChapters[chapterNum] = true

							// Try to extract title
							title := extractTitleFromDirName(dirName)
							if title != "" {
								chapterTitles[chapterNum] = title
								discoveredTitles[chapterNum] = title
								logger.Info(fmt.Sprintf("Found title for chapter %g: %s", chapterNum, title))
							}
						}
					}
					continue
				}

				// Also collect parent directories
				dirPath := filepath.Dir(f.Name)
				if dirPath != "." && dirPath != "" {
					directories[dirPath] = true
				}

				if util.IsImageFile(f.Name) {
					// Check filename for chapter markers
					matches := util.ChapterPattern.FindStringSubmatch(filepath.Base(f.Name))
					if len(matches) > 1 {
						if chapterNum, err := strconv.ParseFloat(matches[1], 64); err == nil {
							neededChapters[chapterNum] = true
						}
					}

					// Check path for folder-based chapters
					dirPath := filepath.Dir(f.Name)
					if dirPath != "." {
						// Analyze each directory component
						parts := strings.Split(dirPath, "/")
						for _, part := range parts {
							if part == "" {
								continue
							}

							// Try regular chapter pattern
							matches := util.ChapterPattern.FindStringSubmatch(part)
							if len(matches) > 1 {
								if chapterNum, err := strconv.ParseFloat(matches[1], 64); err == nil {
									neededChapters[chapterNum] = true
									chapterDirs[dirPath] = chapterNum

									// Try to extract title from directory
									title := extractTitleFromDirName(part)
									if title != "" {
										chapterTitles[chapterNum] = title
										discoveredTitles[chapterNum] = title
										logger.Info(fmt.Sprintf("Found title for chapter %g: %s", chapterNum, title))
									}
								}
								continue
							}

							// Try folder pattern which is more specific for directory names
							matches = util.FolderChapterPattern.FindStringSubmatch(part)
							if len(matches) > 1 {
								if chapterNum, err := strconv.ParseFloat(matches[1], 64); err == nil {
									neededChapters[chapterNum] = true
									chapterDirs[dirPath] = chapterNum

									// Try to extract title from directory
									title := extractTitleFromDirName(part)
									if title != "" {
										chapterTitles[chapterNum] = title
										discoveredTitles[chapterNum] = title
										logger.Info(fmt.Sprintf("Found title for chapter %g: %s", chapterNum, title))
									}
								}
							}
						}
					}
				}
			}

			// Detailed debug logging for detected chapter directories
			if len(chapterDirs) > 0 {
				logger.Info(fmt.Sprintf("Found %d chapter directories in %s", len(chapterDirs), baseName))
				for dir, chNum := range chapterDirs {
					title := "unknown"
					if t, exists := chapterTitles[chNum]; exists {
						title = t
					}
					logger.Info(fmt.Sprintf("  - Chapter %g in directory: %s (Title: %s)", chNum, dir, title))
				}
			}
		}
	}

	// Second pass: check individual chapter files
	for _, filePath := range cbzFiles {
		baseName := filepath.Base(filePath)

		// Only check chapter files (not volumes)
		if !util.VolumePattern.MatchString(baseName) {
			chapterNum := float64(util.ExtractChapterNumber(baseName))
			if chapterNum >= 0 {
				neededChapters[chapterNum] = true
			}
		}
	}

	// Add any discovered titles to the chapter titles cache
	if len(discoveredTitles) > 0 {
		logger.Info(fmt.Sprintf("Found %d chapter titles from directory structure", len(discoveredTitles)))
		for chNum, title := range discoveredTitles {
			logger.Info(fmt.Sprintf("  - Chapter %g: %s", chNum, title))
			// Here you would add to config.ChapterTitles if it had access to it
			// Since we can't modify the config directly, we'll keep this for now and pass it later
		}
	}

	remainingNeeded := len(neededChapters) - len(discoveredTitles)
	if remainingNeeded > 0 {
		logger.Info(fmt.Sprintf("Still need %d more chapter titles", remainingNeeded))
	} else if len(neededChapters) > 0 {
		logger.Info("All needed chapter titles were found from directory structure")
	}

	return neededChapters, discoveredTitles
}

// Helper function to extract title from directory name
func extractTitleFromDirName(dirName string) string {
	// Try different patterns to extract title part

	// Common pattern: "Chapter X - Title"
	re1 := regexp.MustCompile(`(?i)Chapter\s+\d+\s*[-:]\s*(.+)$`)
	if match := re1.FindStringSubmatch(dirName); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	// Pattern for "Ch X - Title"
	re2 := regexp.MustCompile(`(?i)Ch\.?\s+\d+\s*[-:]\s*(.+)$`)
	if match := re2.FindStringSubmatch(dirName); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	// Generic pattern: just get what's after the hyphen if there is one
	re3 := regexp.MustCompile(`\d+\s*[-:]\s*(.+)$`)
	if match := re3.FindStringSubmatch(dirName); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	return ""
}
