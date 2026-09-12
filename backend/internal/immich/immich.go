// Package immich resolves and downloads video assets from a self-hosted Immich
// instance, either by asset id (needs an API key) or by a public share link (no
// key required, subject to the link's own allowDownload flag). Immich's API
// sends no CORS headers, so the browser cannot fetch assets directly — the Go
// backend downloads them server-side and feeds the bytes into the same pipeline
// a browser upload would.
package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config points at one Immich instance.
type Config struct {
	BaseURL string // e.g. https://photos.example.com (no trailing slash)
	APIKey  string // used for ResolveAsset / owned-asset downloads
}

// Enabled reports whether enough config is present to attempt a resolve.
func (c Config) Enabled() bool { return c.BaseURL != "" }

// Asset is the subset of Immich's asset metadata this app needs, plus enough
// to perform the actual download.
type Asset struct {
	ID               string
	Type             string // "VIDEO", "IMAGE", ...
	OriginalFileName string
	OriginalMimeType string
	DurationSec      float64
	SizeBytes        int64
	Description      string // current asset caption, if any (for write-back: don't clobber it)

	downloadURL string
	useAPIKey   bool // true: send x-api-key header; false: URL already carries ?key=
}

// Client talks to one Immich instance.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a Client. cfg.BaseURL is trimmed of a trailing slash.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// ExtractShareKey pulls the opaque share key out of a pasted value, which may
// be a bare key, a full https://host/share/<key> URL, or a URL carrying
// ?key=<key>. Returns the input trimmed if no recognisable shape is found.
func ExtractShareKey(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return s
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		if k := u.Query().Get("key"); k != "" {
			return k
		}
		if i := strings.LastIndex(u.Path, "/share/"); i >= 0 {
			return strings.Trim(u.Path[i+len("/share/"):], "/")
		}
		// some other URL shape: fall through to the raw string
	}
	return s
}

type assetResponse struct {
	ID               string       `json:"id"`
	Type             string       `json:"type"`
	OriginalFileName string       `json:"originalFileName"`
	OriginalMimeType string       `json:"originalMimeType"`
	DurationMs       *json.Number `json:"duration"`
	ExifInfo         struct {
		FileSizeInByte int64  `json:"fileSizeInByte"`
		Description    string `json:"description"`
	} `json:"exifInfo"`
}

// ResolveAsset fetches metadata for one asset the API key can see. The
// returned Asset downloads via the x-api-key header.
func (c *Client) ResolveAsset(ctx context.Context, assetID string) (Asset, error) {
	if c.cfg.APIKey == "" {
		return Asset{}, fmt.Errorf("immich: IMMICH_API_KEY not configured")
	}
	var raw assetResponse
	if err := c.getJSON(ctx, c.cfg.BaseURL+"/api/assets/"+url.PathEscape(assetID), true, "", &raw); err != nil {
		return Asset{}, fmt.Errorf("resolve asset %s: %w", assetID, err)
	}
	a := toAsset(raw)
	a.downloadURL = c.cfg.BaseURL + "/api/assets/" + url.PathEscape(a.ID) + "/original"
	a.useAPIKey = true
	return a, nil
}

type shareLinkResponse struct {
	AllowDownload bool            `json:"allowDownload"`
	Assets        []assetResponse `json:"assets"`
}

// ResolveShareLink resolves a public share link (no API key needed). If the
// share contains multiple assets, assetIDHint picks one by id; otherwise the
// first VIDEO asset in the share is used. The returned Asset downloads via the
// share key as a query parameter.
func (c *Client) ResolveShareLink(ctx context.Context, shareLinkOrKey, assetIDHint string) (Asset, error) {
	key := ExtractShareKey(shareLinkOrKey)
	if key == "" {
		return Asset{}, fmt.Errorf("immich: empty share link")
	}
	var link shareLinkResponse
	if err := c.getJSON(ctx, c.cfg.BaseURL+"/api/shared-links/me?key="+url.QueryEscape(key), false, "", &link); err != nil {
		return Asset{}, fmt.Errorf("resolve share link: %w", err)
	}
	if !link.AllowDownload {
		return Asset{}, fmt.Errorf("immich: this share link has downloads disabled")
	}
	raw, err := pickAsset(link.Assets, assetIDHint)
	if err != nil {
		return Asset{}, err
	}
	a := toAsset(raw)
	a.downloadURL = c.cfg.BaseURL + "/api/assets/" + url.PathEscape(a.ID) + "/original?key=" + url.QueryEscape(key)
	a.useAPIKey = false
	return a, nil
}

func pickAsset(assets []assetResponse, hint string) (assetResponse, error) {
	if hint != "" {
		for _, a := range assets {
			if a.ID == hint {
				return a, nil
			}
		}
		return assetResponse{}, fmt.Errorf("immich: asset %s not found in this share link", hint)
	}
	for _, a := range assets {
		if a.Type == "VIDEO" {
			return a, nil
		}
	}
	if len(assets) == 0 {
		return assetResponse{}, fmt.Errorf("immich: share link has no assets")
	}
	return assetResponse{}, fmt.Errorf("immich: share link has no VIDEO asset (pass assetId to pick one explicitly)")
}

func toAsset(raw assetResponse) Asset {
	var durSec float64
	if raw.DurationMs != nil {
		if f, err := raw.DurationMs.Float64(); err == nil {
			durSec = f / 1000.0
		}
	}
	return Asset{
		ID:               raw.ID,
		Type:             raw.Type,
		OriginalFileName: raw.OriginalFileName,
		OriginalMimeType: raw.OriginalMimeType,
		DurationSec:      durSec,
		SizeBytes:        raw.ExifInfo.FileSizeInByte,
		Description:      raw.ExifInfo.Description,
	}
}

// Download streams the asset's original bytes to dstPath.
func (c *Client) Download(ctx context.Context, a Asset, dstPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.downloadURL, nil)
	if err != nil {
		return err
	}
	if a.useAPIKey {
		req.Header.Set("x-api-key", c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("immich download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("immich download: HTTP %d: %s", resp.StatusCode, body)
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("immich download: %w", err)
	}
	return out.Sync()
}

// UpdateDescription sets an asset's caption/description (shown in Immich's own
// asset info panel). Requires the configured API key — there is no share-key
// equivalent for write operations.
func (c *Client) UpdateDescription(ctx context.Context, assetID, description string) error {
	if c.cfg.APIKey == "" {
		return fmt.Errorf("immich: IMMICH_API_KEY not configured")
	}
	body, err := json.Marshal(map[string]string{"description": description})
	if err != nil {
		return err
	}
	return c.putJSON(ctx, c.cfg.BaseURL+"/api/assets/"+url.PathEscape(assetID), body, nil)
}

// UpsertMetadata writes one arbitrary structured key/value pair into an
// asset's custom-metadata sidecar (PUT /assets/{id}/metadata), Immich's
// free-form per-asset extension store — a separate mechanism from the
// human-readable description, meant for tooling to round-trip structured data.
func (c *Client) UpsertMetadata(ctx context.Context, assetID, key string, value any) error {
	if c.cfg.APIKey == "" {
		return fmt.Errorf("immich: IMMICH_API_KEY not configured")
	}
	body, err := json.Marshal(map[string]any{
		"items": []map[string]any{{"key": key, "value": value}},
	})
	if err != nil {
		return err
	}
	return c.putJSON(ctx, c.cfg.BaseURL+"/api/assets/"+url.PathEscape(assetID)+"/metadata", body, nil)
}

func (c *Client) putJSON(ctx context.Context, rawURL string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// getJSON performs an authenticated (or public, for share links) GET and
// decodes a JSON body. Downloads use a long-lived client; metadata calls get a
// bounded timeout via ctx.
func (c *Client) getJSON(ctx context.Context, rawURL string, useAPIKey bool, apiKeyOverride string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if useAPIKey {
		key := c.cfg.APIKey
		if apiKeyOverride != "" {
			key = apiKeyOverride
		}
		req.Header.Set("x-api-key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
