# API Path Finder

A simple Go tool to find API endpoints by scanning URLs from local files and testing them on a target server.

- Reads URL paths from `.txt` files in `web-content/`
- Sends HTTP requests and detects valid API responses (JSON or other data)
- Prints each discovered URL live to the terminal
- Saves results in a JSON file named after the base URL

## usage

1. Put URL paths in `.txt` files inside the `web-content` folder.
2. Run the program and enter the base URL when prompted.
3. View discovered URLs in the terminal as they are found.
4. Check the generated JSON file for full results.