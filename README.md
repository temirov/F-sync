# F-Sync Relationship Matrix

F-Sync compares relationship exports from two Twitter archive ZIP files and renders an interactive HTML matrix highlighting mutuals, one-way followers, and block lists.

## HTTP server mode

You can launch an HTTP server that renders the comparison interface on demand. Provide the paths to the two exported ZIP files and optionally enable handle resolution against twitter.com:

```bash
go run ./cmd/server --zip-a /path/to/first.zip --zip-b /path/to/second.zip --port 8080
```

The server listens on `127.0.0.1` by default; use `--host` to override the bind address. Add `--resolve-handles` to fetch missing handles over HTTPS before rendering the page.

Health information is available at `http://<host>:<port>/healthz`, and the rendered comparison is served at the root path.
