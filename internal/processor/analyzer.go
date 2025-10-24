package processor

import (
	"archive/zip"
	"fmt"
	"manga-tool/internal/util"
	"path/filepath"
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

			// First, collect all directories
			for _, f := range reader.File {
				if f.FileInfo().IsDir() {
					directories[f.Name] = true
				}
				// Also collect parent directories
				dirPath := filepath.Dir(f.Name)
				if dirPath != "." && dirPath != "" {
					directories[dirPath] = true
				}
			}

			// Now, for each unique directory, check for chapter markers
			for dir := range directories {
				dirName := filepath.Base(dir)
				chapterNum := -1.0
				found := false
				folderMatch := util.FolderChapterPattern.FindStringSubmatch(dirName)
				if len(folderMatch) > 1 {
					chapterNum, _ = strconv.ParseFloat(folderMatch[1], 64)
					found = true
				} else {
					numMatch := util.NumPattern.FindStringSubmatch(dirName)
					if len(numMatch) > 1 {
						chapterNum, _ = strconv.ParseFloat(numMatch[1], 64)
						found = true
					}
				}
				if found {
					chapterDirs[dir] = chapterNum
					neededChapters[chapterNum] = true
					title := ""
					titleMatch := util.ChapterTitlePattern.FindStringSubmatch(dirName)
					if len(titleMatch) > 2 {
						title = strings.TrimSpace(titleMatch[2])
					}
					chapterTitles[chapterNum] = title
					discoveredTitles[chapterNum] = title
					logger.Info(fmt.Sprintf("Found chapter %g: %s", chapterNum, title))
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

	// Check non-volume files or chapter markers
	for _, filePath := range cbzFiles {
		baseName := filepath.Base(filePath)
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
