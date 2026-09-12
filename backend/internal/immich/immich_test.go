package immich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestExtractShareKey(t *testing.T) {
	cases := map[string]string{
		"fakeShareKey1234567890abcdefGHIJKLMNOPQRSTUVWXYZfakekeyEND":            "fakeShareKey1234567890abcdefGHIJKLMNOPQRSTUVWXYZfakekeyEND",
		"https://photos.example.com/share/fakeShareKey1234567890abcdefGHIJKLMN": "fakeShareKey1234567890abcdefGHIJKLMN",
		"https://photos.example.com/share/abc/":                                 "abc",
		"https://photos.example.com/api/shared-links/me?key=xyz123":             "xyz123",
		"  padded-key  ": "padded-key",
		"":               "",
	}
	for in, want := range cases {
		if got := ExtractShareKey(in); got != want {
			t.Errorf("ExtractShareKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// realAssetJSON mirrors the actual response shape Immich 3.1.0 returns for
// GET /api/assets/{id} — duration is bare milliseconds, not the HH:MM:SS
// string some other Immich API docs describe.
const realAssetJSON = `{
  "id": "00000000-0000-4000-8000-000000000001",
  "type": "VIDEO",
  "originalFileName": "clip.mov",
  "originalMimeType": "video/quicktime",
  "duration": 33632,
  "exifInfo": { "fileSizeInByte": 250587102 }
}`

func TestResolveAsset_ParsesRealShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing/wrong x-api-key header: %q", r.Header.Get("x-api-key"))
		}
		if r.URL.Path != "/api/assets/00000000-0000-4000-8000-000000000001" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(realAssetJSON))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"})
	a, err := c.ResolveAsset(context.Background(), "00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "VIDEO" || a.OriginalFileName != "clip.mov" {
		t.Fatalf("unexpected asset: %+v", a)
	}
	if a.DurationSec != 33.632 {
		t.Fatalf("duration: want 33.632s, got %v", a.DurationSec)
	}
	if a.SizeBytes != 250587102 {
		t.Fatalf("size: want 250587102, got %v", a.SizeBytes)
	}
	if a.downloadURL != srv.URL+"/api/assets/00000000-0000-4000-8000-000000000001/original" || !a.useAPIKey {
		t.Fatalf("download config wrong: url=%s useAPIKey=%v", a.downloadURL, a.useAPIKey)
	}
}

func TestResolveShareLink_PicksFirstVideoAndRespectsAllowDownload(t *testing.T) {
	body, _ := json.Marshal(shareLinkResponse{
		AllowDownload: true,
		Assets: []assetResponse{
			{ID: "img-1", Type: "IMAGE", OriginalFileName: "a.jpg"},
			{ID: "vid-1", Type: "VIDEO", OriginalFileName: "b.mp4"},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "shk" {
			t.Errorf("expected key=shk, got %s", r.URL.RawQuery)
		}
		if r.Header.Get("x-api-key") != "" {
			t.Errorf("share-link resolution must not send x-api-key, got %q", r.Header.Get("x-api-key"))
		}
		w.Write(body)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	a, err := c.ResolveShareLink(context.Background(), srv.URL+"/share/shk", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "vid-1" || a.useAPIKey {
		t.Fatalf("expected the VIDEO asset picked with useAPIKey=false, got %+v", a)
	}
	if a.downloadURL != srv.URL+"/api/assets/vid-1/original?key=shk" {
		t.Fatalf("unexpected downloadURL: %s", a.downloadURL)
	}
}

func TestResolveShareLink_NoDownloadAllowed(t *testing.T) {
	body, _ := json.Marshal(shareLinkResponse{AllowDownload: false, Assets: []assetResponse{{ID: "x", Type: "VIDEO"}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	_, err := New(Config{BaseURL: srv.URL}).ResolveShareLink(context.Background(), "key", "")
	if err == nil {
		t.Fatal("expected an error when allowDownload is false")
	}
}

func TestResolveShareLink_NoVideoAsset(t *testing.T) {
	body, _ := json.Marshal(shareLinkResponse{AllowDownload: true, Assets: []assetResponse{{ID: "x", Type: "IMAGE"}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	_, err := New(Config{BaseURL: srv.URL}).ResolveShareLink(context.Background(), "key", "")
	if err == nil {
		t.Fatal("expected an error when the share link has no VIDEO asset")
	}
}

func TestResolveShareLink_AssetIDHintDisambiguates(t *testing.T) {
	body, _ := json.Marshal(shareLinkResponse{
		AllowDownload: true,
		Assets: []assetResponse{
			{ID: "vid-1", Type: "VIDEO"},
			{ID: "vid-2", Type: "VIDEO"},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	a, err := New(Config{BaseURL: srv.URL}).ResolveShareLink(context.Background(), "key", "vid-2")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "vid-2" {
		t.Fatalf("hint not honoured: got %s", a.ID)
	}
}

func TestDownload_StreamsBodyAndSendsAuth(t *testing.T) {
	const payload = "fake video bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key on download, got %q", r.Header.Get("x-api-key"))
		}
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"})
	a := Asset{ID: "x", downloadURL: srv.URL + "/api/assets/x/original", useAPIKey: true}
	dst := t.TempDir() + "/out.bin"
	if err := c.Download(context.Background(), a, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != payload {
		t.Fatalf("downloaded %q, want %q", b, payload)
	}
}

func TestUpdateDescription_SendsPUTWithAuth(t *testing.T) {
	var gotMethod, gotKey, gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotKey, gotPath = r.Method, r.Header.Get("x-api-key"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"})
	if err := c.UpdateDescription(context.Background(), "asset-1", "a new caption"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotKey != "test-key" || gotPath != "/api/assets/asset-1" {
		t.Fatalf("method=%s key=%s path=%s", gotMethod, gotKey, gotPath)
	}
	if gotBody["description"] != "a new caption" {
		t.Fatalf("unexpected body: %+v", gotBody)
	}
}

func TestUpdateDescription_RequiresAPIKey(t *testing.T) {
	c := New(Config{BaseURL: "http://unused.invalid"})
	if err := c.UpdateDescription(context.Background(), "asset-1", "x"); err == nil {
		t.Fatal("expected an error without an API key")
	}
}

func TestUpsertMetadata_SendsItemsArray(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"})
	err := c.UpsertMetadata(context.Background(), "asset-1", "video-extractor", map[string]any{"story": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/assets/asset-1/metadata" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	items, _ := gotBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one item, got %+v", gotBody)
	}
	item, _ := items[0].(map[string]any)
	if item["key"] != "video-extractor" {
		t.Fatalf("unexpected key: %+v", item)
	}
	value, _ := item["value"].(map[string]any)
	if value["story"] != "hi" {
		t.Fatalf("unexpected value: %+v", item)
	}
}

func TestPutJSON_NonSuccessStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"nope"}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "test-key"})
	if err := c.UpdateDescription(context.Background(), "asset-1", "x"); err == nil {
		t.Fatal("expected an error on a non-2xx response")
	}
}
