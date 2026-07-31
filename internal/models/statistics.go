package models

import "encoding/xml"

// Totals is the appliance-wide VStotals block. Every field is already a
// per-second rate, so these become gauges, never counters.
type Totals struct {
	ConnsPerSec Num `xml:"ConnsPerSec" json:"ConnsPerSec"`
	BytesPerSec Num `xml:"BytesPerSec" json:"BytesPerSec"`
	PktsPerSec  Num `xml:"PktsPerSec"  json:"PktsPerSec"`
}

// VirtualService is one Vs element of the stats payload. Note it carries no
// service name — that comes from listvs and is joined on address:port.
type VirtualService struct {
	Index        int    `xml:"Index"        json:"Index"`
	Address      string `xml:"VSAddress"    json:"VSAddress"`
	Port         int    `xml:"VSPort"       json:"VSPort"`
	Protocol     string `xml:"VSProt"       json:"VSProt"`
	TotalConns   Num    `xml:"TotalConns"   json:"TotalConns"`
	TotalPkts    Num    `xml:"TotalPkts"    json:"TotalPkts"`
	TotalBytes   Num    `xml:"TotalBytes"   json:"TotalBytes"`
	ActiveConns  Num    `xml:"ActiveConns"  json:"ActiveConns"`
	ConnsPerSec  Num    `xml:"ConnsPerSec"  json:"ConnsPerSec"`
	BytesRead    Num    `xml:"BytesRead"    json:"BytesRead"`
	BytesWritten Num    `xml:"BytesWritten" json:"BytesWritten"`
	Enable       Num    `xml:"Enable"       json:"Enable"`
}

// RealServer is one Rs element. VSIndex ties it back to its virtual service.
type RealServer struct {
	VSIndex      int    `xml:"VSIndex"      json:"VSIndex"`
	RSIndex      int    `xml:"RSIndex"      json:"RSIndex"`
	Address      string `xml:"Addr"         json:"Addr"`
	Port         int    `xml:"Port"         json:"Port"`
	TotalConns   Num    `xml:"Conns"        json:"Conns"`
	TotalPkts    Num    `xml:"Pkts"         json:"Pkts"`
	TotalBytes   Num    `xml:"Bytes"        json:"Bytes"`
	ActiveConns  Num    `xml:"ActiveConns"  json:"ActiveConns"`
	ConnsPerSec  Num    `xml:"ConnsPerSec"  json:"ConnsPerSec"`
	BytesRead    Num    `xml:"BytesRead"    json:"BytesRead"`
	BytesWritten Num    `xml:"BytesWritten" json:"BytesWritten"`
	Status       string `xml:"Status"       json:"Status"`
}

// CPU is one processor row. ID is "total" for the aggregate and "cpuN" per core.
type CPU struct {
	ID     string `xml:"-" json:"id"`
	User   Num    `xml:"User"   json:"User"`
	System Num    `xml:"System" json:"System"`
	Idle   Num    `xml:"Idle"   json:"Idle"`
}

// Memory is the appliance memory block. Kemp reports bytes here, not kilobytes.
type Memory struct {
	UsedBytes   Num `xml:"memused"        json:"memused"`
	UsedPercent Num `xml:"percentmemused" json:"percentmemused"`
	FreeBytes   Num `xml:"memfree"        json:"memfree"`
}

// TPS holds transactions per second. Despite the name these are instantaneous
// rates, so they map to gauges without a _total suffix.
type TPS struct {
	Total Num `xml:"Total" json:"Total"`
	SSL   Num `xml:"SSL"   json:"SSL"`
}

// Interface is one network interface row.
type Interface struct {
	ID           string `xml:"-" json:"id"`
	BytesRead    Num    `xml:"bytesread"    json:"bytesread"`
	BytesWritten Num    `xml:"byteswritten" json:"byteswritten"`
}

// cpuSection decodes the name-keyed XML <CPU> block into a flat slice.
type cpuSection []CPU

// netSection decodes the name-keyed XML <Network> block into a flat slice.
type netSection []Interface

// Statistics is the decoded stats payload.
//
// The CPU and Network sections are keyed by element NAME in XML (<total>, <cpu0>,
// <eth0>) but are arrays with an "id" field in JSON. Custom UnmarshalXML methods on
// cpuSection and networkSection reconcile the two into the same slice shape, so both
// transports produce an identical Statistics.
type Statistics struct {
	Totals          Totals           `xml:"VStotals" json:"VStotals"`
	VirtualServices []VirtualService `xml:"Vs"       json:"Vs"`
	RealServers     []RealServer     `xml:"Rs"       json:"Rs"`
	CPUs            cpuSection       `xml:"CPU"      json:"CPU"`
	Memory          Memory           `xml:"Memory"   json:"Memory"`
	Interfaces      netSection       `xml:"Network"  json:"Network"`
	TPS             TPS              `xml:"TPS"      json:"TPS"`
}

// UnmarshalXML walks the child elements of <CPU>, using each element's own name
// as the cpu ID.
func (c *cpuSection) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var cpu CPU
			if err := d.DecodeElement(&cpu, &t); err != nil {
				return err
			}
			cpu.ID = t.Name.Local
			*c = append(*c, cpu)
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

// UnmarshalXML walks the child elements of <Network>, using each element's own
// name as the interface ID.
func (n *netSection) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var iface Interface
			if err := d.DecodeElement(&iface, &t); err != nil {
				return err
			}
			iface.ID = t.Name.Local
			*n = append(*n, iface)
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}
