package kemp

import "strconv"

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
		{Key: "system", Value: system},
	}
}

// cpuLabels identifies one processor row; id is "total" or "cpuN".
func cpuLabels(system, id string) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "cpu", Value: id},
	}
}

// interfaceLabels identifies one network interface.
func interfaceLabels(system, iface string) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "interface", Value: iface},
	}
}

// vsLabels is the canonical virtual-service label set.
//
// All five keys are always present. An unresolved name yields an empty VALUE, never
// a missing key: a metric name must carry one label-key set across every series, or
// the Prometheus collector rejects the divergent ones.
func vsLabels(system, name, address string, port int, protocol string) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "name", Value: name},
		{Key: "address", Value: address},
		{Key: "port", Value: strconv.Itoa(port)},
		{Key: "protocol", Value: protocol},
	}
}

// rsLabels is the canonical real-server label set. vs_address and vs_port let a
// dashboard group real servers under their virtual service.
func rsLabels(system, address string, port int, vsAddress string, vsPort int) []Label {
	return []Label{
		{Key: "system", Value: system},
		{Key: "address", Value: address},
		{Key: "port", Value: strconv.Itoa(port)},
		{Key: "vs_address", Value: vsAddress},
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
	return append(out, Label{Key: key, Value: value})
}
