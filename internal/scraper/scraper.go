package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	// NOTE: If you get import errors for this package, run: go get golang.org/x/net
	// This needs to be installed on your Ubuntu server with: go get -u golang.org/x/net/html
	"golang.org/x/net/html"
)

// Logger interface for handling logs
type Logger interface {
	Info(msg string)
	Warning(msg string)
	Error(msg string)
}

// WebInput is a function type for getting user input
type WebInput func(prompt, inputType string) string

// MangaSource represents a manga source
type MangaSource struct {
	URL        string
	SourceType string
}

// ChapterTitles is a map of chapter numbers to titles
type ChapterTitles map[float64]string

// SavedSources maps manga titles to their sources
type SavedSources map[string]map[string]string

// ChapterPattern is the regex for matching chapter titles
var ChapterPattern = regexp.MustCompile(`Chapter (\d+(?:\.\d+)?):?\s*(.+?)$`)

// RedundantPrefixPattern strips redundant "Chapter {number} - " prefix
var RedundantPrefixPattern = regexp.MustCompile(`^Chapter\s+\d+(?:\.\d+)?\s*-\s*`)

// ID extraction for MangaDex
var MangaDexIDRegex = regexp.MustCompile(`/title/([0-9a-fA-F-]+)`)

// LoadSources loads saved sources from a JSON file
func LoadSources(filename string) (SavedSources, error) {
	sources := make(SavedSources)
	data, err := os.ReadFile(filename)
	if err != nil {
		return sources, err
	}

	if err := json.Unmarshal(data, &sources); err != nil {
		return sources, err
	}

	return sources, nil
}

// SaveSources saves sources to a JSON file
func SaveSources(filename string, sources SavedSources) error {
	data, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}

// GetCachedSources gets cached sources for a manga title
func GetCachedSources(filename, mangaTitle string) map[string]string {
	sources, err := LoadSources(filename)
	if err != nil {
		return nil
	}

	return sources[mangaTitle]
}

// SaveMangaSources saves manga sources for a title
func SaveMangaSources(filename, mangaTitle, mangareaderURL, mangadexURL string) error {
	sources, err := LoadSources(filename)
	if err != nil {
		sources = make(SavedSources)
	}

	if _, ok := sources[mangaTitle]; !ok {
		sources[mangaTitle] = make(map[string]string)
	}

	if mangareaderURL != "" {
		sources[mangaTitle]["mangareader"] = mangareaderURL
	}

	if mangadexURL != "" {
		sources[mangaTitle]["mangadex"] = mangadexURL
	}

	return SaveSources(filename, sources)
}

// GetMangaReaderChapters fetches chapter titles from MangaReader.to
func GetMangaReaderChapters(url string, logger Logger, webInput WebInput) ChapterTitles {
	if logger != nil {
		logger.Info("Fetching chapter titles from MangaReader.to...")
	}

	chapters := make(ChapterTitles)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error creating request: %v", err))
		}
		return chapters
	}

	// Set headers to mimic a browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", "https://mangareader.to/")
	req.Header.Set("Cookie", "chapter-lang=en; isAdult=1; tad=1")

	// Also set cookies the standard way
	cookies := []*http.Cookie{
		{Name: "chapter-lang", Value: "en", Domain: ".mangareader.to", Path: "/"},
		{Name: "isAdult", Value: "1", Domain: ".mangareader.to", Path: "/"},
		{Name: "tad", Value: "1", Domain: ".mangareader.to", Path: "/"},
	}

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error fetching page: %v", err))
		}
		return chapters
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error status code: %d", resp.StatusCode))
		}
		return chapters
	}

	// Efficient single-pass HTML parse for <ul id="en-chapters"> > <li> > <a> > <span class="name">
	doc, err := html.Parse(resp.Body)
	if err != nil {
		if logger != nil {
			logger.Error(fmt.Sprintf("Error parsing HTML: %v", err))
		}
		return chapters
	}

	var enChapters *html.Node
	var f func(*html.Node)
	f = func(n *html.Node) {
		if enChapters != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "ul" {
			for _, attr := range n.Attr {
				if attr.Key == "id" && attr.Val == "en-chapters" {
					enChapters = n
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	if enChapters == nil {
		if logger != nil {
			logger.Error("No English chapter list found (ul#en-chapters)")
		}
		return chapters
	}

	for li := enChapters.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.Data != "li" {
			continue
		}
		var chapterNum float64
		var chapterTitle string
		// Get data-number
		for _, attr := range li.Attr {
			if attr.Key == "data-number" {
				if num, err := strconv.ParseFloat(attr.Val, 64); err == nil {
					chapterNum = num
				}
			}
		}
		if chapterNum == -1 {
			continue
		}
		// Find <span class="name"> inside <a>
		for a := li.FirstChild; a != nil; a = a.NextSibling {
			if a.Type == html.ElementNode && a.Data == "a" {
				for span := a.FirstChild; span != nil; span = span.NextSibling {
					if span.Type == html.ElementNode && span.Data == "span" {
						for _, attr := range span.Attr {
							if attr.Key == "class" && strings.Contains(attr.Val, "name") {
								chapterTitle = getTextContent(span)
							}
						}
					}
				}
			}
		}
		if chapterTitle == "" {
			continue
		}
		// Clean up title
		match := ChapterPattern.FindStringSubmatch(chapterTitle)
		if len(match) > 2 {
			if num, err := strconv.ParseFloat(match[1], 64); err == nil {
				chapterNum = num
			}
			chapterTitle = strings.TrimSpace(match[2])
		}
		chapterTitle = RedundantPrefixPattern.ReplaceAllString(chapterTitle, "")
		chapterTitle = strings.TrimSpace(chapterTitle)
		chapters[chapterNum] = chapterTitle
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d chapter titles from MangaReader.to", len(chapters)))
	}

	return chapters
}

// MangaDexChapterResponse represents the MangaDex API response
type MangaDexChapterResponse struct {
	Result string `json:"result"`
	Data   []struct {
		Attributes struct {
			Chapter string `json:"chapter"`
			Title   string `json:"title"`
		} `json:"attributes"`
	} `json:"data"`
	Total int `json:"total"`
}

// GetMangaDexChapters fetches chapter titles from MangaDex
func GetMangaDexChapters(url string, logger Logger) ChapterTitles {
	if logger != nil {
		logger.Info("Fetching chapter titles from MangaDex...")
	}

	chapters := make(ChapterTitles)

	// Extract manga ID from URL
	match := MangaDexIDRegex.FindStringSubmatch(url)
	if len(match) < 2 {
		if logger != nil {
			logger.Error("Invalid MangaDex link. Could not extract manga ID.")
		}
		return chapters
	}

	mangaID := match[1]
	if logger != nil {
		logger.Info(fmt.Sprintf("Manga ID extracted: %s", mangaID))
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	allChapters := []MangaDexChapterResponse{}
	limit := 100
	offset := 0

	for {
		apiURL := fmt.Sprintf("https://api.mangadex.org/chapter?manga=%s&translatedLanguage[]=en&limit=%d&offset=%d&order[chapter]=asc",
			mangaID, limit, offset)

		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Error creating request: %v", err))
			}
			break
		}

		resp, err := client.Do(req)
		if err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Error fetching chapters: %v", err))
			}
			break
		}

		var data MangaDexChapterResponse
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()

		if err != nil {
			if logger != nil {
				logger.Error(fmt.Sprintf("Error decoding response: %v", err))
			}
			break
		}

		if data.Result != "ok" {
			if logger != nil {
				logger.Error("API returned an error")
			}
			break
		}

		allChapters = append(allChapters, data)

		total := data.Total
		if logger != nil {
			logger.Info(fmt.Sprintf("Retrieved %d/%d chapters...", offset+len(data.Data), total))
		}

		offset += limit
		if offset >= total {
			break
		}
	}

	// Process all chapters
	for _, response := range allChapters {
		for _, ch := range response.Data {
			chapterNumber := ch.Attributes.Chapter
			chapterTitle := ch.Attributes.Title

			if chapterNumber == "" || chapterTitle == "" {
				continue
			}

			// Parse chapter number
			numVal, err := strconv.ParseFloat(chapterNumber, 64)
			if err != nil {
				continue
			}

			chapters[numVal] = chapterTitle
		}
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Found %d chapter titles from MangaDex", len(chapters)))
	}

	return chapters
}

// CollectMissingChapterTitles collects titles for chapters that are missing
func CollectMissingChapterTitles(neededChapters []float64, mangaTitle string, existingTitles ChapterTitles, logger Logger, webInput WebInput) ChapterTitles {
	if webInput == nil {
		return existingTitles
	}

	missingChapters := []float64{}
	for _, ch := range neededChapters {
		if _, ok := existingTitles[ch]; !ok {
			missingChapters = append(missingChapters, ch)
		}
	}

	if len(missingChapters) == 0 {
		return existingTitles
	}

	if logger != nil {
		logger.Info(fmt.Sprintf("Need titles for %d chapters", len(missingChapters)))
		divider := strings.Repeat("=", 70)
		logger.Info("\n" + divider)
		logger.Info(fmt.Sprintf("MANGA: %s - CHAPTER TITLES NEEDED", mangaTitle))
		logger.Info(divider)
		logger.Info("You can enter titles now, or press Enter for default titles.")
		logger.Info("Collect all titles at once to avoid interruptions during processing.")
		logger.Info(divider + "\n")
	}

	result := make(ChapterTitles)
	for k, v := range existingTitles {
		result[k] = v
	}

	for _, chapterNum := range missingChapters {
		prompt := fmt.Sprintf("Title for Chapter %g (or press Enter for default): ", chapterNum)
		title := webInput(prompt, "text")

		if title != "" {
			result[chapterNum] = title
			if logger != nil {
				logger.Info(fmt.Sprintf("Added title for Chapter %g: '%s'", chapterNum, title))
			}
		} else {
			if logger != nil {
				logger.Info(fmt.Sprintf("Using default title for Chapter %g", chapterNum))
			}
		}
	}

	if logger != nil {
		logger.Info("\n" + strings.Repeat("=", 70))
		logger.Info("All chapter titles collected. Starting processing...")
		logger.Info(strings.Repeat("=", 70) + "\n")
	}

	return result
}

// getTextContent gets all text content from a node (used by the parser)
func getTextContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var text string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text += getTextContent(c)
	}
	return text
}
