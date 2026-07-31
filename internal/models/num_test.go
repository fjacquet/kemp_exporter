package models

import (
	"encoding/json"
	"encoding/xml"
	"testing"
)

type numHolder struct {
	V Num `xml:"V" json:"V"`
}

func TestNumXML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantVal float64
		wantOK  bool
	}{
		{"plain", `<numHolder><V>42</V></numHolder>`, 42, true},
		{"zero", `<numHolder><V>0</V></numHolder>`, 0, true},
		{"padded", `<numHolder><V>  17  </V></numHolder>`, 17, true},
		{"float", `<numHolder><V>3.5</V></numHolder>`, 3.5, true},
		{"na", `<numHolder><V>N/A</V></numHolder>`, 0, false},
		{"empty", `<numHolder><V></V></numHolder>`, 0, false},
		{"garbage", `<numHolder><V>abc</V></numHolder>`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h numHolder
			if err := xml.Unmarshal([]byte(tt.in), &h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			gotVal, gotOK := h.V.Get()
			if gotOK != tt.wantOK {
				t.Fatalf("OK = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotVal != tt.wantVal {
				t.Fatalf("Val = %v, want %v", gotVal, tt.wantVal)
			}
		})
	}
}

func TestNumJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantVal float64
		wantOK  bool
	}{
		{"number", `{"V":42}`, 42, true},
		{"zero", `{"V":0}`, 0, true},
		{"string number", `{"V":"17"}`, 17, true},
		{"padded string", `{"V":"  17  "}`, 17, true},
		{"na string", `{"V":"N/A"}`, 0, false},
		{"empty string", `{"V":""}`, 0, false},
		{"null", `{"V":null}`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h numHolder
			if err := json.Unmarshal([]byte(tt.in), &h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			gotVal, gotOK := h.V.Get()
			if gotOK != tt.wantOK {
				t.Fatalf("OK = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotVal != tt.wantVal {
				t.Fatalf("Val = %v, want %v", gotVal, tt.wantVal)
			}
		})
	}
}

// A field the payload omits entirely must be absent, not zero.
func TestNumAbsentField(t *testing.T) {
	var h numHolder
	if err := json.Unmarshal([]byte(`{}`), &h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := h.V.Get(); ok {
		t.Fatal("omitted field reported OK; want absent")
	}
}

// --- Final review, must-fix 1: Num.parse accepted NaN and Inf ---
//
// strconv.ParseFloat accepts "NaN", "Inf", "+Inf", "-Infinity" and friends, so a
// non-finite payload field used to set OK=true and walk straight through the
// absent-never-zero gate into a sample. Prometheus renders NaN, and EVERY alert
// comparison against a NaN is silently false -- a NaN kemp_memory_used_percent
// makes `> 90` permanently untrue, which is the same silent monitoring loss the
// absent-never-zero rule exists to prevent, only harder to see because the series
// is present. A non-finite value is not a reading; it is absent.
func TestNumRejectsNonFinite(t *testing.T) {
	for _, raw := range []string{"NaN", "nan", "Inf", "inf", "+Inf", "-Inf", "Infinity", "-Infinity"} {
		t.Run(raw, func(t *testing.T) {
			var n Num
			n.parse(raw)
			if _, ok := n.Get(); ok {
				t.Fatalf("parse(%q) reported the value present (%v); a non-finite value must be absent, "+
					"or it reaches Prometheus and silently falsifies every alert comparison", raw, n.Val)
			}
		})
	}
}

// The counterpart: ordinary finite values, including negatives and exponents, must
// still parse. The guard must reject non-finite input, not tighten parsing.
func TestNumAcceptsFiniteValues(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
	}{{"0", 0}, {"-1", -1}, {"1e10", 1e10}, {"3.5", 3.5}, {" 42 ", 42}} {
		t.Run(tc.raw, func(t *testing.T) {
			var n Num
			n.parse(tc.raw)
			v, ok := n.Get()
			if !ok || v != tc.want {
				t.Fatalf("parse(%q) = (%v, %v), want (%v, true)", tc.raw, v, ok, tc.want)
			}
		})
	}
}
