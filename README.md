# F-Sync Relationship Matrix

F-Sync compares relationship exports from two Twitter archive ZIP files and renders an interactive HTML matrix highlighting mutuals, one-way followers, and block lists.

## HTTP server mode

You can launch an HTTP server that renders the comparison interface on demand. Provide the paths to the two exported ZIP files:

```bash
go run ./cmd/server --zip-a /path/to/first.zip --zip-b /path/to/second.zip --port 8080
```

The server listens on `127.0.0.1` by default; use `--host` to override the bind address. The server always resolves missing handles before rendering; ensure that Google Chrome or Chromium is installed and discoverable via the `PATH` or `CHROME_BIN` environment variable so the resolver can launch a headless browser session.

Health information is available at `http://<host>:<port>/healthz`, and the rendered comparison is served at the root path.

## Testing

Run the full Go test suite (unit tests plus fast integration checks) with:

```bash
go test ./...
```

The live handle resolution integration test performs real network calls against X/Twitter and requires a headless Chrome or Chromium binary. Provide the browser by ensuring it is discoverable on the `PATH` or by setting the `CHROME_BIN` environment variable. The test is skipped by default to keep routine `go test` runs offline.

Enable the networked integration test explicitly when you have the prerequisites and Internet access:

```bash
go test ./tests -run TestServerHandleResolutionIntegration -server_integration
```

Expect the integration test to take longer than the unit tests because it uploads archives, waits for the comparison job to finish, and validates the rendered HTML using real responses from the X/Twitter intent endpoints.
