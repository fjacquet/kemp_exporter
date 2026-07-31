package kemp

import "github.com/fjacquet/kemp_exporter/internal/models"

// deriveHealth turns the appliance-wide sections of the stats payload — totals,
// CPU, memory, TPS and network interfaces — into samples.
//
// Every value here is either an instantaneous gauge or a cumulative byte counter,
// so nothing depends on the collection interval.
func deriveHealth(system string, st *models.Statistics) []Sample {
	if st == nil {
		return nil
	}
	base := systemLabels(system)
	var out []Sample

	// Totals are already per-second rates: gauges, aggregated with sum/avg in
	// PromQL, never rate().
	out = addSample(out, "kemp_connections_per_second", base, st.Totals.ConnsPerSec)
	out = addSample(out, "kemp_bytes_per_second", base, st.Totals.BytesPerSec)
	out = addSample(out, "kemp_packets_per_second", base, st.Totals.PktsPerSec)

	for _, cpu := range st.CPUs {
		labels := cpuLabels(system, cpu.ID)
		out = addSample(out, "kemp_cpu_idle_percent", labels, cpu.Idle)
		out = addSample(out, "kemp_cpu_user_percent", labels, cpu.User)
		out = addSample(out, "kemp_cpu_system_percent", labels, cpu.System)
	}

	// Percentages stay on the 0-100 scale Kemp reports; no /100 conversion.
	out = addSample(out, "kemp_memory_free_bytes", base, st.Memory.FreeBytes)
	out = addSample(out, "kemp_memory_used_bytes", base, st.Memory.UsedBytes)
	out = addSample(out, "kemp_memory_used_percent", base, st.Memory.UsedPercent)

	// Transactions per second are rates despite the field name, so no _total suffix.
	out = addSample(out, "kemp_tps", base, st.TPS.Total)
	out = addSample(out, "kemp_tps_ssl", base, st.TPS.SSL)

	for _, iface := range st.Interfaces {
		labels := interfaceLabels(system, iface.ID)
		out = addSample(out, "kemp_interface_bytes_read_total", labels, iface.BytesRead)
		out = addSample(out, "kemp_interface_bytes_written_total", labels, iface.BytesWritten)
	}

	return out
}
