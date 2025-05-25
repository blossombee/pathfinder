# Path Finder

Path Finder is a Go program that discovers API endpoints by scanning URLs from text files and performing HTTP requests with parameter fuzzing. It also supports recursive discovery by parsing JavaScript and HTML content for additional paths.

## Features

- Reads URLs from `.txt` files in a `Discovery` folder
- Sends HTTP requests with multiple methods and parameter payloads
- Filters results based on HTTP status and response content
- Parses JavaScript and HTML responses to find new API paths
- Supports concurrency with configurable worker count
- Shows progress bar and outputs results to a file and table

## Usage

```bash
go run main.go
