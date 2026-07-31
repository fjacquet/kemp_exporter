// Package models holds the decoded LoadMaster payload types shared by both
// wire transports (XML and JSON).
package models

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"math"
	"strconv"
	"strings"
)

// Num is a numeric payload field that records whether it actually parsed.
//
// LoadMaster returns numbers as XML chardata; older firmware pads them with
// whitespace, and a disabled subsystem yields "" or "N/A". Decoding those into a
// plain float64 would silently produce 0, which is indistinguishable from a real
// zero reading. Num keeps that distinction so derivations can omit the sample
// entirely rather than publish a fabricated value.
type Num struct {
	Val float64
	OK  bool
}

// Get returns the value and whether it parsed. Callers must check the bool and
// skip emitting a sample when it is false.
func (n Num) Get() (float64, bool) { return n.Val, n.OK }

// parse fills n from a raw payload string. Unparseable input leaves n absent
// rather than returning an error: one bad field must not fail the whole decode.
func (n *Num) parse(raw string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return
	}
	// strconv.ParseFloat accepts "NaN", "Inf", "+Inf" and "-Infinity", which would
	// otherwise set OK=true and carry a non-finite value through the
	// absent-never-zero gate into a real sample. Prometheus renders NaN, and every
	// alert comparison against a NaN evaluates false -- a NaN
	// kemp_memory_used_percent makes `> 90` permanently untrue -- so a non-finite
	// value is the same silent monitoring loss a fabricated 0 would be, only harder
	// to spot because the series exists. It is not a reading: treat it as absent.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	n.Val, n.OK = v, true
}

// UnmarshalXML decodes chardata into the tolerant representation.
func (n *Num) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	n.parse(raw)
	return nil
}

// UnmarshalJSON accepts a JSON number, a stringified number, or null. Kemp's JSON
// mode is not consistent about which it uses for a given field.
func (n *Num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n.parse(s)
		return nil
	}
	n.parse(string(b))
	return nil
}
