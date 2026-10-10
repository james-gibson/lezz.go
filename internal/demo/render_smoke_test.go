package demo

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderBaseSmokeAlarmConfig(t *testing.T) {
	var buf bytes.Buffer
	if err := baseSmokeAlarmConfigTmpl.Execute(&buf, baseSmokeAlarmConfig{
		Port:               19101,
		ListenAddr:         "0.0.0.0",
		StateDir:           "/tmp/lezz-base/state-a",
		Binary:             "/tmp/ocd-smoke-alarm",
		ConfigPath:         "/tmp/lezz-base/alarm-a.yaml",
		LogPath:            "/tmp/lezz-base/alarm-a.log",
		ClusterID:          baseClusterID(),
		FederationBasePort: 5100,
		FederationMaxPort:  5107,
	}); err != nil {
		t.Fatalf("execute base template: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"targets: []",
		"discovery:",
		"enabled: true",
		"federation:",
		"cluster_id: \"lezz-base:5100-5107\"",
		"base_port: 5100",
		"max_port: 5107",
		"dynamic_config:",
		"remote_agent:",
		"managed_updates: true",
		"listen_addr: \"0.0.0.0:19101\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered base config missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "peer:") {
		t.Errorf("base config must not hardcode a peer target:\n%s", out)
	}
}
