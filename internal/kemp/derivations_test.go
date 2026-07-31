package kemp

import (
	"encoding/xml"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

func decodeStats(t *testing.T, name string) *models.Statistics {
	t.Helper()
	var wrapper struct {
		XMLName xml.Name          `xml:"Response"`
		Data    models.Statistics `xml:"Success>Data"`
	}
	if err := xml.Unmarshal(fixture(t, name), &wrapper); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return &wrapper.Data
}

func decodeVSInfo(t *testing.T, name string) []models.VirtualServiceInfo {
	t.Helper()
	var wrapper struct {
		XMLName xml.Name                    `xml:"Response"`
		VS      []models.VirtualServiceInfo `xml:"Success>Data>VS"`
	}
	if err := xml.Unmarshal(fixture(t, name), &wrapper); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return wrapper.VS
}

// findSample returns the first sample with the given name and label values.
func findSample(samples []Sample, name string, labelValues ...string) (Sample, bool) {
	for _, s := range samples {
		if s.Name != name {
			continue
		}
		if len(labelValues) == 0 {
			return s, true
		}
		match := true
		for i, want := range labelValues {
			if i >= len(s.Labels) || s.Labels[i].Value != want {
				match = false
				break
			}
		}
		if match {
			return s, true
		}
	}
	return Sample{}, false
}

func hasSample(samples []Sample, name string) bool {
	_, ok := findSample(samples, name)
	return ok
}

// Two virtual services on one address must each get their OWN name. The upstream
// exporter keyed its lookup on address alone and silently mislabelled one of them.
func TestDeriveVSJoinsOnAddressAndPort(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	info := decodeVSInfo(t, "listvs.xml")
	samples := deriveVirtualServices("lm-01", st, info)

	s443, ok := findSample(samples, "kemp_virtual_service_active_connections", "lm-01", "web-https", "10.0.0.10", "443", "tcp")
	if !ok {
		t.Fatalf("no active_connections sample for web-https:443; got %+v", samples)
	}
	if s443.Value != 42 {
		t.Errorf("web-https active connections = %v, want 42", s443.Value)
	}
	if _, ok := findSample(samples, "kemp_virtual_service_active_connections", "lm-01", "web-http", "10.0.0.10", "80", "tcp"); !ok {
		t.Error("no active_connections sample for web-http:80 — the join collapsed two ports onto one name")
	}
}

// Status maps to the binary _up gauge and to the verbatim _status info metric.
func TestDeriveVSStatusMapping(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	info := decodeVSInfo(t, "listvs.xml")
	samples := deriveVirtualServices("lm-01", st, info)

	up443, ok := findSample(samples, "kemp_virtual_service_up", "lm-01", "web-https", "10.0.0.10", "443", "tcp")
	if !ok || up443.Value != 1 {
		t.Errorf("web-https up = %+v, want value 1 (status Up)", up443)
	}
	// Sick is degraded but still serving, so it counts as up.
	up80, ok := findSample(samples, "kemp_virtual_service_up", "lm-01", "web-http", "10.0.0.10", "80", "tcp")
	if !ok || up80.Value != 1 {
		t.Errorf("web-http up = %+v, want value 1 (status Sick still serves)", up80)
	}
	// The verbatim status survives as an info metric with a sixth label.
	stat, ok := findSample(samples, "kemp_virtual_service_status", "lm-01", "web-http", "10.0.0.10", "80", "tcp", "Sick")
	if !ok {
		t.Fatalf("no kemp_virtual_service_status sample carrying status=Sick; got %+v", samples)
	}
	if stat.Value != 1 {
		t.Errorf("status info metric value = %v, want 1", stat.Value)
	}
	if len(stat.Labels) != 6 || stat.Labels[5].Key != "status" {
		t.Errorf("status labels = %+v, want six keys ending in \"status\"", stat.Labels)
	}
}

func TestStatusToUp(t *testing.T) {
	tests := []struct {
		status string
		want   float64
		ok     bool
	}{
		{"Up", 1, true},
		{"up", 1, true},
		{"Sick", 1, true},
		{"Redirect", 1, true},
		{"Down", 0, true},
		{"Disabled", 0, true},
		{"", 0, false},
		{"Bananas", 0, false},
	}
	for _, tt := range tests {
		got, ok := statusToUp(tt.status)
		if ok != tt.ok {
			t.Errorf("statusToUp(%q) ok = %v, want %v", tt.status, ok, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("statusToUp(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// The core invariant: an unparseable field yields NO sample. A fabricated 0 on a
// connection count is indistinguishable from a healthy idle service.
func TestDeriveVSOmitsUnparseableFields(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveVirtualServices("lm-01", st, nil)

	// TotalConns=7 parsed, so its counter is present.
	if s, ok := findSample(samples, "kemp_virtual_service_connections_total"); !ok || s.Value != 7 {
		t.Errorf("connections_total = %+v, want value 7", s)
	}
	// ActiveConns=0 is a REAL zero and must be emitted.
	if s, ok := findSample(samples, "kemp_virtual_service_active_connections"); !ok || s.Value != 0 {
		t.Errorf("active_connections = %+v, want a real 0 to be emitted", s)
	}
	// TotalPkts=N/A, TotalBytes="", ConnsPerSec=N/A must all be absent.
	for _, name := range []string{
		"kemp_virtual_service_packets_total",
		"kemp_virtual_service_bytes_total",
		"kemp_virtual_service_connections_per_second",
	} {
		if hasSample(samples, name) {
			t.Errorf("%s was emitted for an unparseable field; want absent", name)
		}
	}
}

// A virtual service present in stats but absent from listvs keeps every label KEY
// with an empty name value, so the metric family holds one label-key set.
func TestDeriveVSUnresolvedNameKeepsLabelKeys(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveVirtualServices("lm-01", st, nil)

	s, ok := findSample(samples, "kemp_virtual_service_connections_total")
	if !ok {
		t.Fatal("no connections_total sample")
	}
	if len(s.Labels) != 5 {
		t.Fatalf("labels = %+v, want 5 keys even with no listvs entry", s.Labels)
	}
	if s.Labels[1].Key != "name" || s.Labels[1].Value != "" {
		t.Errorf("labels[1] = %+v, want key \"name\" with empty value", s.Labels[1])
	}
}

// With no listvs entry there is no status, so neither status metric may appear.
func TestDeriveVSNoStatusWithoutListVS(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveVirtualServices("lm-01", st, nil)
	for _, name := range []string{"kemp_virtual_service_up", "kemp_virtual_service_status"} {
		if hasSample(samples, name) {
			t.Errorf("%s emitted with no listvs data; want absent", name)
		}
	}
}

func TestDeriveRealServersLinksToVirtualService(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	info := decodeVSInfo(t, "listvs.xml")
	samples := deriveRealServers("lm-01", st, info)

	s, ok := findSample(samples, "kemp_real_server_active_connections", "lm-01", "192.168.1.20", "8443", "10.0.0.10", "443")
	if !ok {
		t.Fatalf("no real-server sample linked to its virtual service; got %+v", samples)
	}
	if s.Value != 21 {
		t.Errorf("active connections = %v, want 21", s.Value)
	}
	if len(s.Labels) != 5 {
		t.Errorf("labels = %+v, want 5 keys", s.Labels)
	}
}

// Carry-forward from Task 7: models.RealServer.Status is absent from both stats.xml
// and stats.json, so kemp_real_server_up / kemp_real_server_status went completely
// untested and a struct-tag mismatch on that one field could pass silently. The
// hostile fixture's three Rs entries (Status Up, Down, Bogus) close that gap: one
// status statusToUp maps to 1, one it maps to 0, and one it does not recognise at
// all — which must suppress kemp_real_server_up while still preserving the verbatim
// string on the _status info metric.
func TestDeriveRealServersStatusMapping(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveRealServers("lm-01", st, nil)

	up, ok := findSample(samples, "kemp_real_server_up", "lm-01", "192.168.1.30", "8080")
	if !ok || up.Value != 1 {
		t.Errorf("RS Status=Up -> kemp_real_server_up = %+v, want value 1", up)
	}
	down, ok := findSample(samples, "kemp_real_server_up", "lm-01", "192.168.1.31", "8080")
	if !ok || down.Value != 0 {
		t.Errorf("RS Status=Down -> kemp_real_server_up = %+v, want value 0", down)
	}
	if _, ok := findSample(samples, "kemp_real_server_up", "lm-01", "192.168.1.32", "8080"); ok {
		t.Error("RS Status=Bogus (unrecognised) must yield no kemp_real_server_up sample")
	}

	// The unrecognised status is not evidence of failure, but it is still real data:
	// the info metric preserves it verbatim so an operator can see the appliance
	// reported something the exporter doesn't know how to classify.
	stat, ok := findSample(samples, "kemp_real_server_status", "lm-01", "192.168.1.32", "8080")
	if !ok {
		t.Fatalf("no kemp_real_server_status sample for the unrecognised-status real server; got %+v", samples)
	}
	if stat.Value != 1 || len(stat.Labels) != 6 || stat.Labels[5].Key != "status" || stat.Labels[5].Value != "Bogus" {
		t.Errorf("status info metric = %+v, want six labels ending in status=\"Bogus\" with value 1", stat)
	}
}
