package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/common-nighthawk/go-figure"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
)

var (
	notFoundFingerprint string
	totalFiles          int
	totalURLs           int
	apiEndpoints        int
	apiFoundMutex       sync.Mutex
	seenURLs            = make(map[string]bool)
	seenMutex           sync.Mutex
)

var parameterPayloads = []string{"?id=1", "?user=admin", "?q=test"}

type APIResult struct {
	URL     string `json:"url"`
	Method  string `json:"method"`
	Status  int    `json:"status"`
	Snippet string `json:"snippet"`
}

func main() {
	reader := bufio.NewReader(os.Stdin)

	// Colors
	infoColor := color.New(color.FgHiMagenta)
	errorColor := color.New(color.FgRed)

	baseDir := "web-content"
	if fi, err := os.Stat(baseDir); err != nil || !fi.IsDir() {
		errorColor.Printf("Error: Base directory '%s' does not exist or is not accessible.\n", baseDir)
		return
	}

	var baseURL string
	for {
		fmt.Print("Enter base URL (required): ")
		baseURLInput, _ := reader.ReadString('\n')
		baseURL = strings.TrimSpace(baseURLInput)
		if baseURL != "" {
			break
		}
		errorColor.Println("Base URL cannot be empty.")
	}

	baseURL = ensureScheme(baseURL)

	fmt.Print("Enter number of workers (default 20): ")
	workerNumInput, _ := reader.ReadString('\n')
	workerNum := 20
	if trimmed := strings.TrimSpace(workerNumInput); trimmed != "" {
		if w, err := strconv.Atoi(trimmed); err == nil && w > 0 {
			workerNum = w
		} else {
			errorColor.Println("Invalid worker number; using default 20.")
		}
	}

	fmt.Print("Enter delay between requests in milliseconds (default 0): ")
	delayInput, _ := reader.ReadString('\n')
	delay := time.Duration(0)
	if trimmed := strings.TrimSpace(delayInput); trimmed != "" {
		if ms, err := strconv.Atoi(trimmed); err == nil {
			delay = time.Duration(ms) * time.Millisecond
		}
	}

	jsonFileName := sanitizeFilename(baseURL) + "_found_apis.json"

	fmt.Println()
	fmt.Println()

	// ASCII art title in purple
	purple := color.New(color.FgMagenta, color.Bold)
	figure := figure.NewFigure("Path Finder", "", true)
	asciiLines := strings.Split(figure.String(), "\n")
	for _, line := range asciiLines {
		purple.Println(line)
	}
	fmt.Println()
	infoColor.Println("Starting URL discovery...")

	notFoundFingerprint = getNotFoundFingerprint(baseURL + "/this_path_should_not_exist_123456789")

	urls, err := collectURLs(baseDir, baseURL)
	if err != nil {
		errorColor.Printf("Error collecting URLs: %v\n", err)
		return
	}
	totalURLs = len(urls)

	infoColor.Printf("Found %d URLs to scan from %d files.\n", totalURLs, totalFiles)

	bar := pb.StartNew(totalURLs)
	linesChan := make(chan string, 1000)
	discoveredChan := make(chan string, 1000)

	seenMutex.Lock()
	for _, url := range urls {
		seenURLs[url] = true
	}
	seenMutex.Unlock()

	// Slice to store results for JSON output
	var results []APIResult
	var resultsMutex sync.Mutex

	client := &http.Client{Timeout: 10 * time.Second}

	methods := []string{"GET", "HEAD"}
	headers := map[string]string{} // Add token/cookie if needed
	allowedStatus := map[int]bool{200: true, 201: true, 204: true}

	var wg sync.WaitGroup
	var tableData [][]string
	var tableMutex sync.Mutex

	for i := 0; i < workerNum; i++ {
		wg.Add(1)
		go worker(linesChan, &wg, bar, &results, &resultsMutex, &tableData, &tableMutex, client, methods, allowedStatus, headers, delay, discoveredChan)
	}

	go func() {
		for _, url := range urls {
			linesChan <- url
		}
		close(linesChan)
	}()

	go func() {
		for newPath := range discoveredChan {
			fullURL := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(newPath, "/")
			seenMutex.Lock()
			if !seenURLs[fullURL] {
				seenURLs[fullURL] = true
				linesChan <- fullURL
			}
			seenMutex.Unlock()
		}
	}()

	wg.Wait()
	bar.Finish()

	// Write all results to JSON file
	file, err := os.Create(jsonFileName)
	if err != nil {
		errorColor.Printf("Error creating output JSON file: %v\n", err)
		return
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	err = enc.Encode(results)
	if err != nil {
		errorColor.Printf("Error encoding JSON: %v\n", err)
		return
	}

	fmt.Println()
	renderTable(tableData)
	infoColor.Printf("\nScan complete.\nTotal files scanned: %d\nTotal URLs checked: %d\nAPI endpoints found: %d\nResults saved to %s\n",
		totalFiles, totalURLs, apiEndpoints, jsonFileName)
}

func ensureScheme(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}

	tryURL := "https://" + rawURL
	resp, err := http.Head(tryURL)
	if err == nil && resp.StatusCode < 400 {
		resp.Body.Close()
		return tryURL
	}

	// fallback to http
	return "http://" + rawURL
}

func sanitizeFilename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		rawURL = strings.ReplaceAll(rawURL, "://", "_")
		rawURL = strings.ReplaceAll(rawURL, "/", "_")
		return rawURL
	}

	host := u.Hostname()
	path := strings.ReplaceAll(u.Path, "/", "_")
	if path == "" {
		path = "root"
	}
	filename := host + path
	filename = regexp.MustCompile(`[^a-zA-Z0-9_\-\.]`).ReplaceAllString(filename, "")
	return filename
}

func collectURLs(baseDir, baseURL string) ([]string, error) {
	var urls []string
	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("Error accessing %q: %v\n", path, err)
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".txt") {
			totalFiles++
			file, err := os.Open(path)
			if err != nil {
				fmt.Printf("Could not open file %s: %v\n", path, err)
				return nil
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line != "" {
					fullURL := fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), strings.TrimLeft(line, "/"))
					urls = append(urls, fullURL)
				}
			}
		}
		return nil
	})
	return urls, err
}

func getNotFoundFingerprint(url string) string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	return string(bodyBytes)
}

func sameContent(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

func discoverFromJSorHTML(urlStr string, body string) []string {
	var discovered []string
	re := regexp.MustCompile(`(?i)(/api/[\w/-]+)`)
	matches := re.FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		discovered = append(discovered, strings.TrimSpace(m[1]))
	}
	return discovered
}

func worker(
	linesChan <-chan string,
	wg *sync.WaitGroup,
	bar *pb.ProgressBar,
	results *[]APIResult,
	resultsMutex *sync.Mutex,
	tableData *[][]string,
	tableMutex *sync.Mutex,
	client *http.Client,
	methods []string,
	allowedStatus map[int]bool,
	headers map[string]string,
	delay time.Duration,
	discoveredChan chan<- string,
) {
	defer wg.Done()

	for url := range linesChan {
		for _, method := range methods {
			fuzzedURLs := []string{url}
			for _, payload := range parameterPayloads {
				fuzzedURLs = append(fuzzedURLs, url+payload)
			}
			for _, fuzzURL := range fuzzedURLs {
				req, _ := http.NewRequest(method, fuzzURL, nil)
				for k, v := range headers {
					req.Header.Set(k, v)
				}
				resp, err := client.Do(req)
				if err != nil {
					bar.Increment()
					continue
				}
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				bodyStr := string(bodyBytes)

				contentType := resp.Header.Get("Content-Type")
				if !allowedStatus[resp.StatusCode] || sameContent(bodyStr, notFoundFingerprint) ||
					strings.Contains(strings.ToLower(contentType), "html") {
					bar.Increment()
					continue
				}

				apiFoundMutex.Lock()
				apiEndpoints++
				apiFoundMutex.Unlock()

				// Prepare snippet
				snippet := bodyStr
				if len(snippet) > 100 {
					snippet = snippet[:100] + "..."
				}

				// Print immediately to terminal
				fmt.Printf("[FOUND] %s %s %d\n", fuzzURL, method, resp.StatusCode)

				// Save result to slice
				resultsMutex.Lock()
				*results = append(*results, APIResult{
					URL:     fuzzURL,
					Method:  method,
					Status:  resp.StatusCode,
					Snippet: snippet,
				})
				resultsMutex.Unlock()

				// Add to table for summary
				tableMutex.Lock()
				*tableData = append(*tableData, []string{fuzzURL, method, strconv.Itoa(resp.StatusCode), snippet})
				tableMutex.Unlock()

				// Discover new paths from JSON or JS (optional)
				if strings.Contains(strings.ToLower(contentType), "javascript") || strings.Contains(strings.ToLower(contentType), "json") {
					discovered := discoverFromJSorHTML(fuzzURL, bodyStr)
					for _, u := range discovered {
						discoveredChan <- u
					}
				}

				bar.Increment()
				time.Sleep(delay)
			}
		}
	}
}

func renderTable(data [][]string) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"URL", "Method", "Status", "Response Snippet"})
	table.SetBorder(true)
	table.SetRowLine(true)
	table.SetAutoWrapText(false)
	table.SetHeaderColor(
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgHiMagentaColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgHiMagentaColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgHiMagentaColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgHiMagentaColor},
	)
	table.SetColumnColor(
		tablewriter.Colors{tablewriter.FgMagentaColor},
		tablewriter.Colors{tablewriter.FgHiMagentaColor},
		tablewriter.Colors{tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.FgHiBlueColor},
	)
	for _, row := range data {
		table.Append(row)
	}
	table.Render()
}
