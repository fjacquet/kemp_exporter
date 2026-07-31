package kemp

import (
	"strconv"
	"strings"
)

// cleanValue makes an appliance-supplied string safe to use as a label value,
// replacing any invalid UTF-8 byte with U+FFFD.
//
// This is the one place it happens, deliberately upstream of BOTH readers. Label
// values come straight off the wire -- virtual-service name, cpu id, interface id,
// status, addresses -- and the two readers used to disagree about invalid UTF-8:
// prometheus.NewConstMetric rejects it (validateLabelValues), so PromCollector
// dropped that one series and warned, while the OTLP callback exported it happily
// and the protobuf marshal of the export REQUEST then failed with "string field
// contains invalid UTF-8" -- discarding every metric in the batch, every cycle, for
// as long as the appliance reported that name. An OTLP-only deployment lost all
// telemetry behind a single export-error log. Fixing it in each reader would leave
// them deciding independently, which is exactly how that divergence arose.
//
// Replacement, not rejection: a name with one bad byte still identifies a real
// virtual service whose numbers an operator needs, and the substitution character
// is visible in the exported series rather than silently absent. This changes only
// the VALUE; the key set is untouched, so the label-key union invariant is
// unaffected. A valid string is returned byte-identically -- strings.ToValidUTF8
// scans and returns the original when there is nothing to replace.
func cleanValue(v string) string {
	return strings.ToValidUTF8(v, "\uFFFD")
}

// Label is one metric label. Key/Value ordering within a []Label is significant:
// it defines the canonical order for that metric family. The label builders below
// are the only place that order is decided; every derivation elsewhere calls one of
// them rather than constructing a []Label literal, so the canonical order is
// mechanical rather than a review checklist item.
type Label struct {
	Key   string
	Value string
}

// Sample is one exported time-series point, transport- and protocol-agnostic.
// Both the Prometheus collector and the OTLP exporter render from these.
//
// There is no "value present" flag: absence is expressed by not producing a Sample
// at all. An unparseable numeric field yields no sample, never a fabricated 0.
type Sample struct {
	Name   string
	Labels []Label
	Value  float64
}

// systemLabels is the base label set every system-scoped metric carries.
func systemLabels(system string) []Label {
	return []Label{
		{Key: "system", Value: cleanValue(system)},
	}
}

// cpuLabels identifies one processor row; id is "total" or "cpuN".
func cpuLabels(system, id string) []Label {
	return []Label{
		{Key: "system", Value: cleanValue(system)},
		{Key: "cpu", Value: cleanValue(id)},
	}
}

// interfaceLabels identifies one network interface.
func interfaceLabels(system, iface string) []Label {
	return []Label{
		{Key: "system", Value: cleanValue(system)},
		{Key: "interface", Value: cleanValue(iface)},
	}
}

// vsLabels is the canonical virtual-service label set.
//
// All five keys are always present. An unresolved name yields an empty VALUE, never
// a missing key: a metric name must carry one label-key set across every series, or
// the Prometheus collector rejects the divergent ones.
func vsLabels(system, name, address string, port int, protocol string) []Label {
	return []Label{
		{Key: "system", Value: cleanValue(system)},
		{Key: "name", Value: cleanValue(name)},
		{Key: "address", Value: cleanValue(address)},
		{Key: "port", Value: strconv.Itoa(port)},
		{Key: "protocol", Value: cleanValue(protocol)},
	}
}

// rsLabels is the canonical real-server label set. vs_address and vs_port let a
// dashboard group real servers under their virtual service.
func rsLabels(system, address string, port int, vsAddress string, vsPort int) []Label {
	return []Label{
		{Key: "system", Value: cleanValue(system)},
		{Key: "address", Value: cleanValue(address)},
		{Key: "port", Value: strconv.Itoa(port)},
		{Key: "vs_address", Value: cleanValue(vsAddress)},
		{Key: "vs_port", Value: strconv.Itoa(vsPort)},
	}
}

// withLabel returns a copy of base with one key appended, for the *_status info
// metrics whose family carries the base set plus a single extra `status` key. It
// never mutates base: base may be shared by other samples derived from the same
// call site, and appending in place could silently overwrite or alias their backing
// array.
func withLabel(base []Label, key, value string) []Label {
	out := make([]Label, len(base), len(base)+1)
	copy(out, base)
	return append(out, Label{Key: key, Value: cleanValue(value)})
}
