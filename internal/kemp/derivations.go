package kemp

import (
	"strconv"
	"strings"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// vsKey is the join key between the stats and listvs payloads.
//
// Keying on address alone is wrong: one virtual IP commonly hosts several ports
// (80 and 443 on the same VIP is the default web pattern), and an address-only
// lookup silently gives both services the same name.
func vsKey(address string, port int) string {
	return address + ":" + strconv.Itoa(port)
}

// indexVSInfo builds the address:port lookup used to resolve service names.
func indexVSInfo(info []models.VirtualServiceInfo) map[string]models.VirtualServiceInfo {
	idx := make(map[string]models.VirtualServiceInfo, len(info))
	for _, v := range info {
		idx[vsKey(v.Address, v.Port)] = v
	}
	return idx
}

// statusToUp maps a LoadMaster status string onto the binary _up gauge.
//
// The mapping is total and deliberate. "Sick" and "Redirect" both still serve
// traffic, so they count as up; "Disabled" is administratively out of rotation and
// counts as down. An unrecognised status returns ok=false so the caller omits the
// sample entirely — an unknown status is not evidence of failure, and reporting it
// as 0 would fire a false outage alert.
func statusToUp(status string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "up", "sick", "redirect":
		return 1, true
	case "down", "disabled":
		return 0, true
	default:
		return 0, false
	}
}

// addSample appends a sample only when the source field parsed. This is the single
// choke point for the absent-never-zero policy: every numeric value this package
// emits — whether lifted straight from a models.Num field or computed here (the
// _up/_status values below) — is wrapped and passed through this one function, so
// there is exactly one place that can get "absent vs. zero" wrong.
func addSample(out []Sample, name string, labels []Label, n models.Num) []Sample {
	v, ok := n.Get()
	if !ok {
		return out
	}
	return append(out, Sample{Name: name, Labels: labels, Value: v})
}

// deriveVirtualServices turns the stats payload's Vs entries into samples, joining
// each against listvs for its name and status.
func deriveVirtualServices(system string, st *models.Statistics, info []models.VirtualServiceInfo) []Sample {
	if st == nil {
		return nil
	}
	idx := indexVSInfo(info)
	var out []Sample

	for _, vs := range st.VirtualServices {
		meta := idx[vsKey(vs.Address, vs.Port)] // zero value gives an unresolved name/status/protocol

		// The stats payload's own VSProt is the live, per-scrape reading and wins
		// whenever present. listvs's Protocol is a fallback, not a competing source
		// of truth: an appliance that blanks VSProt on a stats row can still report
		// the service's protocol via listvs, and falling back beats silently
		// degrading the label to empty.
		protocol := vs.Protocol
		if protocol == "" {
			protocol = meta.Protocol
		}
		labels := vsLabels(system, meta.Name, vs.Address, vs.Port, protocol)

		out = addSample(out, "kemp_virtual_service_active_connections", labels, vs.ActiveConns)
		out = addSample(out, "kemp_virtual_service_connections_per_second", labels, vs.ConnsPerSec)
		out = addSample(out, "kemp_virtual_service_connections_total", labels, vs.TotalConns)
		out = addSample(out, "kemp_virtual_service_packets_total", labels, vs.TotalPkts)
		out = addSample(out, "kemp_virtual_service_bytes_total", labels, vs.TotalBytes)
		out = addSample(out, "kemp_virtual_service_bytes_read_total", labels, vs.BytesRead)
		out = addSample(out, "kemp_virtual_service_bytes_written_total", labels, vs.BytesWritten)

		// Status metrics require listvs; without it there is nothing to report and
		// guessing from Enable would conflate "administratively enabled" with "up".
		if meta.Status == "" {
			continue
		}
		// statusToUp's own (value, ok) pair is exactly a models.Num's shape, so
		// passing it straight through addSample makes addSample the single place
		// that decides absence as well as the single place a Sample is built —
		// there is no separate "if ok" gate outside it to keep in sync.
		v, ok := statusToUp(meta.Status)
		out = addSample(out, "kemp_virtual_service_up", labels, models.Num{Val: v, OK: ok})
		out = addSample(out, "kemp_virtual_service_status", withLabel(labels, "status", meta.Status), models.Num{Val: 1, OK: true})
	}
	return out
}

// deriveRealServers turns the stats payload's Rs entries into samples, linking each
// back to its virtual service through VSIndex.
func deriveRealServers(system string, st *models.Statistics, info []models.VirtualServiceInfo) []Sample {
	if st == nil {
		return nil
	}
	// Map VSIndex -> virtual service identity, preferring the stats payload's own
	// Vs entries and falling back to listvs.
	type vsIdentity struct {
		address string
		port    int
	}
	vsByIndex := make(map[int]vsIdentity, len(st.VirtualServices)+len(info))
	for _, vs := range st.VirtualServices {
		vsByIndex[vs.Index] = vsIdentity{vs.Address, vs.Port}
	}
	for _, v := range info {
		if _, ok := vsByIndex[v.Index]; !ok {
			vsByIndex[v.Index] = vsIdentity{v.Address, v.Port}
		}
	}

	var out []Sample
	for _, rs := range st.RealServers {
		parent := vsByIndex[rs.VSIndex] // zero value gives empty address, port 0
		labels := rsLabels(system, rs.Address, rs.Port, parent.address, parent.port)

		out = addSample(out, "kemp_real_server_active_connections", labels, rs.ActiveConns)
		out = addSample(out, "kemp_real_server_connections_per_second", labels, rs.ConnsPerSec)
		out = addSample(out, "kemp_real_server_connections_total", labels, rs.TotalConns)
		out = addSample(out, "kemp_real_server_packets_total", labels, rs.TotalPkts)
		out = addSample(out, "kemp_real_server_bytes_total", labels, rs.TotalBytes)
		out = addSample(out, "kemp_real_server_bytes_read_total", labels, rs.BytesRead)
		out = addSample(out, "kemp_real_server_bytes_written_total", labels, rs.BytesWritten)

		if rs.Status == "" {
			continue
		}
		v, ok := statusToUp(rs.Status)
		out = addSample(out, "kemp_real_server_up", labels, models.Num{Val: v, OK: ok})
		out = addSample(out, "kemp_real_server_status", withLabel(labels, "status", rs.Status), models.Num{Val: 1, OK: true})
	}
	return out
}
