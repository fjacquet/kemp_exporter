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
