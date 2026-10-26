package demo

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRegistryServer(t *testing.T, seed ClusterInfo) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newDiscoveryMux(newRegistry(seed)))
	t.Cleanup(srv.Close)
	return srv
}

func getClusters(t *testing.T, baseURL string) map[string]ClusterInfo {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/cluster", http.NoBody)
	if err != nil {
		t.Fatalf("new GET request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /cluster: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /cluster status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var got map[string]ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode /cluster: %v", err)
	}
	return got
}

func doRequest(t *testing.T, method, url string, body []byte) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new %s request: %v", method, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestDiscoveryDeleteClusterByName is the lezz.go#57 regression: with only the
// exact "/cluster" pattern registered, ServeMux answered DELETE
// /cluster/{name} with its own 404 and the handler never ran.
func TestDiscoveryDeleteClusterByName(t *testing.T) {
	srv := newTestRegistryServer(t, ClusterInfo{Name: "demo-123"})

	if code := doRequest(t, http.MethodDelete, srv.URL+"/cluster/demo-123", nil); code != http.StatusOK {
		t.Fatalf("DELETE /cluster/demo-123 status = %d, want %d", code, http.StatusOK)
	}

	if got := getClusters(t, srv.URL); len(got) != 0 {
		t.Fatalf("registry after delete = %v, want empty", got)
	}
}

// TestDiscoveryRenameByPostThenDelete covers the reason the route exists: a
// cluster adopts a stable name, then drops its demo-<pid> entry, leaving one
// entry for the pair of alarms.
func TestDiscoveryRenameByPostThenDelete(t *testing.T) {
	srv := newTestRegistryServer(t, ClusterInfo{Name: "demo-123"})

	kb, err := json.Marshal(ClusterInfo{Name: "kb"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if code := doRequest(t, http.MethodPost, srv.URL+"/cluster", kb); code != http.StatusOK {
		t.Fatalf("POST /cluster status = %d, want %d", code, http.StatusOK)
	}
	if code := doRequest(t, http.MethodDelete, srv.URL+"/cluster/demo-123", nil); code != http.StatusOK {
		t.Fatalf("DELETE /cluster/demo-123 status = %d, want %d", code, http.StatusOK)
	}

	got := getClusters(t, srv.URL)
	if _, ok := got["demo-123"]; ok {
		t.Fatalf("registry still lists demo-123: %v", got)
	}
	if _, ok := got["kb"]; !ok {
		t.Fatalf("registry does not list kb: %v", got)
	}
}

// TestDiscoveryCollectionRejectsSubpaths checks the subtree registration did not
// widen the collection endpoints: only DELETE may address a name.
func TestDiscoveryCollectionRejectsSubpaths(t *testing.T) {
	srv := newTestRegistryServer(t, ClusterInfo{Name: "demo-123"})

	if code := doRequest(t, http.MethodGet, srv.URL+"/cluster/demo-123", nil); code != http.StatusNotFound {
		t.Errorf("GET /cluster/demo-123 status = %d, want %d", code, http.StatusNotFound)
	}
	if code := doRequest(t, http.MethodDelete, srv.URL+"/cluster", nil); code != http.StatusBadRequest {
		t.Errorf("DELETE /cluster status = %d, want %d", code, http.StatusBadRequest)
	}
	if got := getClusters(t, srv.URL); len(got) != 1 {
		t.Fatalf("registry = %v, want the seed left untouched", got)
	}
}
