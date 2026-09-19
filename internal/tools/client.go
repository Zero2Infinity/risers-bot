// This file owns the HTTP transport to DCL: one shared DCLClient per
// process, GetJSON for single fetches and GetPaginated for offset-paged
// list endpoints. No business logic, no caching, no tool-specific shapes —
// just transport. Each tool file calls client.GetJSON or GetPaginated.
//
// Depends only on stdlib net/http + encoding/json. Never imports llm,
// agent, db, or history.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout bounds one DCL fetch. DCL is a small JSON API; 10s is
// generous without hanging a WhatsApp turn.
const defaultTimeout = 10 * time.Second

// maxErrorSnippet caps how much of a non-200 body lands in the error.
// Matches the ollama client convention (4KB snippet) so logs stay small.
const maxErrorSnippet = 4096

// maxPages caps pagination loops. Verified page sizes are 10-20 rows, so 20
// pages cover 200-400 rows; anything beyond is a server bug — fail loud
// instead of looping forever inside a WhatsApp turn.
const maxPages = 20

// DCLClient is a thin HTTP+JSON wrapper for the DCL API. One instance per
// process; reuse across all tools. Config selects team + base URL.
type DCLClient struct {
	config Config
	http   *http.Client
}

// TeamID returns the configured DCL team (88 = Risers). Tools default
// team-scoped queries to this instead of hardcoding an id.
func (c *DCLClient) TeamID() int {
	return c.config.TeamID
}

// ClientOption applies a functional option to NewDCLClient.
type ClientOption func(*DCLClient)

// WithHTTPClient overrides the HTTP client (httptest in tests).
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cl *DCLClient) { cl.http = c }
}

// NewDCLClient builds a transport. Inject Config from LoadConfig().
func NewDCLClient(cfg Config, opts ...ClientOption) *DCLClient {
	cl := &DCLClient{
		config: cfg,
		http:   &http.Client{Timeout: defaultTimeout},
	}

	for _, o := range opts {
		o(cl)
	}

	return cl
}

// GetJSON GETs config.BaseURL + path and decodes the JSON body into target.
// path must start with "/", e.g. "/api/gettournamentlist". Caller passes a
// pointer (e.g. &result). Wraps transport/decode errors with %w.
func (c *DCLClient) GetJSON(ctx context.Context, path string, target any) error {
	url := c.config.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("dcl new request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dcl get %s: %w", path, err)
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorSnippet))
		return fmt.Errorf("dcl status %d %s: %s", resp.StatusCode, path, snippet)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("dcl read body %s: %w", path, err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("dcl decode %s: %w", path, err)
	}

	return nil
}

// GetPaginated GETs path with offset=N from page 0 upward and returns one
// raw JSON body per non-empty page. key is the envelope list to inspect,
// e.g. "tournamentList" or "teamShedules" (verified: offset is a 0-based
// page index; page sizes differ per endpoint, 10-20 rows; empty list ends
// it). path may carry its own query string (e.g. ?teamId=88, required by
// /api/schedules); offset is appended with the right separator. Returns raw
// pages so tools keep their typed decode; a missing key is an error (fail
// loud on API drift — this caught /api/schedules demanding ?teamId=),
// an empty list stops the loop normally.
func (c *DCLClient) GetPaginated(ctx context.Context, path, key string) ([]json.RawMessage, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var pages []json.RawMessage
	for page := 0; page < maxPages; page++ {
		var raw json.RawMessage
		pagedPath := fmt.Sprintf("%s%soffset=%d", path, sep, page)
		if err := c.GetJSON(ctx, pagedPath, &raw); err != nil {
			return nil, fmt.Errorf("dcl paginate %s page %d: %w", path, page, err)
		}

		var env map[string]json.RawMessage
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("dcl paginate %s page %d envelope: %w", path, page, err)
		}
		listRaw, ok := env[key]
		if !ok {
			return nil, fmt.Errorf("dcl paginate %s: missing key %q", path, key)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(listRaw, &items); err != nil {
			return nil, fmt.Errorf("dcl paginate %s key %q not a list: %w", path, key, err)
		}
		if len(items) == 0 {
			break
		}

		pages = append(pages, raw)
	}

	if len(pages) == maxPages {
		return nil, fmt.Errorf("dcl paginate %s: hit maxPages %d, aborting", path, maxPages)
	}

	return pages, nil
}
