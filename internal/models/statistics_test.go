package models

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
)

// xmlEnvelope mirrors the transport-layer envelope so models can be tested
// against the real fixture shape without importing the kemp package.
type xmlEnvelope struct {
	XMLName xml.Name   `xml:"Response"`
	Stat    string     `xml:"stat,attr"`
	Data    Statistics `xml:"Success>Data"`
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "kemp", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestStatisticsXMLDecode(t *testing.T) {
	var env xmlEnvelope
	if err := xml.Unmarshal(loadFixture(t, "stats.xml"), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	st := env.Data

	if v, ok := st.Totals.ConnsPerSec.Get(); !ok || v != 150 {
		t.Errorf("Totals.ConnsPerSec = %v/%v, want 150/true", v, ok)
	}
	if got := len(st.VirtualServices); got != 2 {
		t.Fatalf("len(VirtualServices) = %d, want 2", got)
	}
	vs := st.VirtualServices[0]
	if vs.Address != "10.0.0.10" || vs.Port != 443 || vs.Protocol != "tcp" {
		t.Errorf("VS[0] identity = %s:%d/%s, want 10.0.0.10:443/tcp", vs.Address, vs.Port, vs.Protocol)
	}
	if v, ok := vs.ActiveConns.Get(); !ok || v != 42 {
		t.Errorf("VS[0].ActiveConns = %v/%v, want 42/true", v, ok)
	}
	if got := len(st.RealServers); got != 1 {
		t.Fatalf("len(RealServers) = %d, want 1", got)
	}
	if st.RealServers[0].VSIndex != 1 {
		t.Errorf("RS[0].VSIndex = %d, want 1", st.RealServers[0].VSIndex)
	}
	if got := len(st.CPUs); got != 2 {
		t.Fatalf("len(CPUs) = %d, want 2 (total + cpu0)", got)
	}
	if st.CPUs[0].ID != "total" {
		t.Errorf("CPUs[0].ID = %q, want \"total\"", st.CPUs[0].ID)
	}
	if v, ok := st.Memory.FreeBytes.Get(); !ok || v != 2147483648 {
		t.Errorf("Memory.FreeBytes = %v/%v, want 2147483648/true", v, ok)
	}
	if got := len(st.Interfaces); got != 1 || st.Interfaces[0].ID != "eth0" {
		t.Errorf("Interfaces = %+v, want one entry with ID eth0", st.Interfaces)
	}
	if v, ok := st.TPS.SSL.Get(); !ok || v != 75 {
		t.Errorf("TPS.SSL = %v/%v, want 75/true", v, ok)
	}
}

func TestVirtualServiceInfoXMLDecode(t *testing.T) {
	var env struct {
		XMLName xml.Name             `xml:"Response"`
		VS      []VirtualServiceInfo `xml:"Success>Data>VS"`
	}
	if err := xml.Unmarshal(loadFixture(t, "listvs.xml"), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := len(env.VS); got != 2 {
		t.Fatalf("len(VS) = %d, want 2", got)
	}
	if env.VS[0].Name != "web-https" || env.VS[0].Port != 443 || env.VS[0].Status != "Up" {
		t.Errorf("VS[0] = %+v, want web-https:443 Up", env.VS[0])
	}
	if env.VS[1].Status != "Sick" {
		t.Errorf("VS[1].Status = %q, want \"Sick\"", env.VS[1].Status)
	}
}
