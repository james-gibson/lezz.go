package demo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAlarmScript rejects any config mentioning cluster_id, mimicking an older
// ocd-smoke-alarm that decodes config strictly.
const fakeAlarmRejectsClusterID = `#!/bin/sh
cfg=""
while [ $# -gt 0 ]; do
  case "$1" in
    -config) cfg="$2"; shift 2;;
    *) shift;;
  esac
done
if grep -q cluster_id "$cfg"; then
  echo "yaml: unmarshal errors: field cluster_id not found" >&2
  exit 1
fi
exit 0
`

// fakeAlarmAcceptsAny always validates successfully.
const fakeAlarmAcceptsAny = `#!/bin/sh
exit 0
`

func writeFakeAlarm(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ocd-smoke-alarm")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake alarm: %v", err)
	}
	return path
}

func testAlarmConfig(port int, clusterID string) baseSmokeAlarmConfig {
	return baseSmokeAlarmConfig{
		Port:               port,
		ListenAddr:         "0.0.0.0",
		StateDir:           "/tmp/lezz-auto-test/state",
		Binary:             "/tmp/ocd-smoke-alarm",
		ConfigPath:         "/tmp/lezz-auto-test/alarm.yaml",
		LogPath:            "/tmp/lezz-auto-test/alarm.log",
		ClusterID:          clusterID,
		FederationBasePort: 5100,
		FederationMaxPort:  5107,
	}
}

func TestWriteValidatedAlarmConfigDegradesOnUnknownField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := writeFakeAlarm(t, fakeAlarmRejectsClusterID)

	path, err := writeValidatedAlarmConfig(context.Background(), dir, "alarm-x", bin, testAlarmConfig(19101, baseClusterID()))
	if err != nil {
		t.Fatalf("writeValidatedAlarmConfig() = %v, want nil (should degrade)", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	if strings.Contains(string(data), "cluster_id") {
		t.Fatalf("cluster_id should have been dropped for an older binary:\n%s", data)
	}
	if !strings.Contains(string(data), "base_port: 5100") {
		t.Fatalf("federation block must survive degradation:\n%s", data)
	}
}

func TestWriteValidatedAlarmConfigKeepsClusterID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := writeFakeAlarm(t, fakeAlarmAcceptsAny)

	path, err := writeValidatedAlarmConfig(context.Background(), dir, "alarm-y", bin, testAlarmConfig(19102, baseClusterID()))
	if err != nil {
		t.Fatalf("writeValidatedAlarmConfig() = %v, want nil", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	if !strings.Contains(string(data), `cluster_id: "lezz-base:5100-5107"`) {
		t.Fatalf("cluster_id must be kept for a compatible binary:\n%s", data)
	}
}

// fakeAlarmRejectsAlways rejects every config, so a genuinely invalid config is
// surfaced rather than silently degraded.
const fakeAlarmRejectsAlways = `#!/bin/sh
echo "boom" >&2
exit 1
`

func TestWriteValidatedAlarmConfigFailsOnHardRejection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := writeFakeAlarm(t, fakeAlarmRejectsAlways)

	_, err := writeValidatedAlarmConfig(context.Background(), dir, "alarm-z", bin, testAlarmConfig(19103, baseClusterID()))
	if err == nil {
		t.Fatal("writeValidatedAlarmConfig() = nil, want an error for a hard rejection")
	}
}
