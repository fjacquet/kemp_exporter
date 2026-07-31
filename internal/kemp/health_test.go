package kemp

import "testing"

func TestDeriveHealthTotalsAreGauges(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	for _, tc := range []struct {
		name string
		want float64
	}{
		{"kemp_connections_per_second", 150},
		{"kemp_bytes_per_second", 2048000},
		{"kemp_packets_per_second", 3200},
	} {
		s, ok := findSample(samples, tc.name, "lm-01")
		if !ok {
			t.Errorf("%s missing", tc.name)
			continue
		}
		if s.Value != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, s.Value, tc.want)
		}
		if len(s.Labels) != 1 || s.Labels[0].Key != "system" {
			t.Errorf("%s labels = %+v, want only {system}", tc.name, s.Labels)
		}
	}
}

func TestDeriveHealthCPUPerRow(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	total, ok := findSample(samples, "kemp_cpu_idle_percent", "lm-01", "total")
	if !ok || total.Value != 80 {
		t.Errorf("cpu total idle = %+v, want value 80", total)
	}
	core, ok := findSample(samples, "kemp_cpu_idle_percent", "lm-01", "cpu0")
	if !ok || core.Value != 77 {
		t.Errorf("cpu0 idle = %+v, want value 77", core)
	}

	// Values matter, not just presence: a swapped field (e.g. user data landing
	// in the system-percent series) would slip past a presence-only check.
	userTotal, ok := findSample(samples, "kemp_cpu_user_percent", "lm-01", "total")
	if !ok || userTotal.Value != 12 {
		t.Errorf("cpu total user = %+v, want value 12", userTotal)
	}
	sysTotal, ok := findSample(samples, "kemp_cpu_system_percent", "lm-01", "total")
	if !ok || sysTotal.Value != 8 {
		t.Errorf("cpu total system = %+v, want value 8", sysTotal)
	}
	userCore, ok := findSample(samples, "kemp_cpu_user_percent", "lm-01", "cpu0")
	if !ok || userCore.Value != 14 {
		t.Errorf("cpu0 user = %+v, want value 14", userCore)
	}
	sysCore, ok := findSample(samples, "kemp_cpu_system_percent", "lm-01", "cpu0")
	if !ok || sysCore.Value != 9 {
		t.Errorf("cpu0 system = %+v, want value 9", sysCore)
	}
}

func TestDeriveHealthMemoryAndTPS(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	for _, tc := range []struct {
		name string
		want float64
	}{
		{"kemp_memory_free_bytes", 2147483648},
		{"kemp_memory_used_bytes", 2147483648},
		{"kemp_memory_used_percent", 50},
		{"kemp_tps", 420},
		{"kemp_tps_ssl", 75},
	} {
		s, ok := findSample(samples, tc.name, "lm-01")
		if !ok {
			t.Errorf("%s missing", tc.name)
			continue
		}
		if s.Value != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, s.Value, tc.want)
		}
	}
	// TPS is a rate, so it must NOT carry the counter suffix.
	if hasSample(samples, "kemp_tps_total") {
		t.Error("kemp_tps_total emitted; TPS is a gauge and must not carry _total")
	}
}

func TestDeriveHealthInterfaces(t *testing.T) {
	st := decodeStats(t, "stats.xml")
	samples := deriveHealth("lm-01", st)

	s, ok := findSample(samples, "kemp_interface_bytes_read_total", "lm-01", "eth0")
	if !ok || s.Value != 987654321 {
		t.Errorf("eth0 bytes read = %+v, want 987654321", s)
	}
	// Value matters, not just presence, so a read/written field swap is caught.
	w, ok := findSample(samples, "kemp_interface_bytes_written_total", "lm-01", "eth0")
	if !ok || w.Value != 123456789 {
		t.Errorf("eth0 bytes written = %+v, want 123456789", w)
	}
}

// An unparseable health field is absent, not zero — a fake 0 on free memory would
// fire a false capacity alert.
func TestDeriveHealthOmitsUnparseableFields(t *testing.T) {
	st := decodeStats(t, "stats_hostile.xml")
	samples := deriveHealth("lm-01", st)

	if s, ok := findSample(samples, "kemp_connections_per_second", "lm-01"); !ok || s.Value != 150 {
		t.Errorf("connections_per_second = %+v, want 150 (whitespace-padded value)", s)
	}
	for _, name := range []string{
		"kemp_bytes_per_second",    // N/A
		"kemp_packets_per_second",  // empty
		"kemp_memory_used_percent", // N/A
		"kemp_memory_free_bytes",   // empty
	} {
		if hasSample(samples, name) {
			t.Errorf("%s emitted for an unparseable field; want absent", name)
		}
	}
	// The section that DID parse is still present.
	if !hasSample(samples, "kemp_memory_used_bytes") {
		t.Error("kemp_memory_used_bytes missing; a sibling field failing must not drop it")
	}
	// The fixture has no TPS, CPU, or Network section at all.
	for _, name := range []string{
		"kemp_tps", "kemp_tps_ssl",
		"kemp_interface_bytes_read_total", "kemp_interface_bytes_written_total",
		"kemp_cpu_idle_percent", "kemp_cpu_user_percent", "kemp_cpu_system_percent",
	} {
		if hasSample(samples, name) {
			t.Errorf("%s emitted with no source section; want absent", name)
		}
	}
}

func TestDeriveHealthNilStatistics(t *testing.T) {
	if got := deriveHealth("lm-01", nil); got != nil {
		t.Errorf("deriveHealth(nil) = %+v, want nil", got)
	}
}
