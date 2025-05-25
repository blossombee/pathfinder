package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/common-nighthawk/go-figure"
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

func main() {
	reader := bufio.NewReader(os.Stdin)

	// Colors
	infoColor := color.New(color.FgHiMagenta)
	errorColor := color.New(color.FgRed)

	baseDir := "Discovery"
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

	fmt.Print("Enter output file name (default found_apis.txt): ")
	outFileInput, _ := reader.ReadString('\n')
	outFile := strings.TrimSpace(outFileInput)
	if outFile == "" {
		outFile = "found_apis.txt"
	}

	fmt.Println()
	fmt.Println()

	// Create a purple color printer for the title
	purple := color.New(color.FgMagenta, color.Bold)

	// Create ASCII art figure
	figure := figure.NewFigure("Path Finder", "", true)
	asciiLines := strings.Split(figure.String(), "\n")

	// Print each ASCII art line in purple
	for _, line := range asciiLines {
		purple.Println(line)
	}

	fmt.Println()
	infoColor.Println("Starting URL discovery...")

	notFoundFingerprint = getNotFoundFingerprint(strings.TrimRight(baseURL, "/") + "/this_path_should_not_exist_123456789")

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

	file, err := os.Create(outFile)
	if err != nil {
		errorColor.Printf("Error creating output file: %v\n", err)
		return
	}
	defer file.Close()

	var tableData [][]string
	var tableMutex sync.Mutex
	client := &http.Client{Timeout: 10 * time.Second}

	methods := []string{"GET", "HEAD"}
	headers := map[string]string{} // Add token/cookie if needed
	allowedStatus := map[int]bool{200: true, 201: true, 204: true}

	var wg sync.WaitGroup
	for i := 0; i < workerNum; i++ {
		wg.Add(1)
		go worker(linesChan, &wg, bar, file, &tableData, &tableMutex, client, methods, allowedStatus, headers, delay, discoveredChan)
	}

	go func() {
		for _, url := range urls {
			linesChan <- url
		}
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

	fmt.Println()
	renderTable(tableData)
	infoColor.Printf("\nScan complete.\nTotal files scanned: %d\nTotal URLs checked: %d\nAPI endpoints found: %d\nResults saved to %s\n",
		totalFiles, totalURLs, apiEndpoints, outFile)
}

// rest of your functions unchanged...

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
		fmt.Printf("Error getting 404 fingerprint: %v\n", err)
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

func worker(linesChan <-chan string, wg *sync.WaitGroup, bar *pb.ProgressBar, file *os.File, tableData *[][]string, tableMutex *sync.Mutex, client *http.Client, methods []string, allowedStatus map[int]bool, headers map[string]string, delay time.Duration, discoveredChan chan<- string) {
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

				if _, ok := allowedStatus[resp.StatusCode]; ok && !sameContent(bodyStr, notFoundFingerprint) {
					apiFoundMutex.Lock()
					apiEndpoints++
					apiFoundMutex.Unlock()

					file.WriteString(fmt.Sprintf("URL: %s\nMethod: %s\nStatus: %d\nResponse:\n%s\n\n-----\n\n", fuzzURL, method, resp.StatusCode, bodyStr))
					snippet := bodyStr
					if len(snippet) > 100 {
						snippet = snippet[:100] + "..."
					}
					tableMutex.Lock()
					*tableData = append(*tableData, []string{fuzzURL, method, strconv.Itoa(resp.StatusCode), snippet})
					tableMutex.Unlock()
				}

				if strings.Contains(resp.Header.Get("Content-Type"), "javascript") || strings.Contains(resp.Header.Get("Content-Type"), "html") {
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
