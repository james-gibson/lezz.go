package demo

import (
	"strings"
	"testing"
)

func TestClusterJoinParamsIncludesProjectID(t *testing.T) {
	data := clusterJoinParams(ClusterInfo{
		Name: "cluster",
		Projects: []ProjectEntry{
			{ID: "01JABCDEF", Name: "pretty", MCPURL: "http://127.0.0.1:1/mcp"},
			{Name: "legacy", MCPURL: "http://127.0.0.1:2/mcp"},
		},
	})
	s := string(data)
	if !strings.Contains(s, `"id":"01JABCDEF"`) {
		t.Fatalf("project id missing: %s", s)
	}
	if !strings.Contains(s, `"id":"legacy"`) {
		t.Fatalf("legacy fallback id missing: %s", s)
	}
}
