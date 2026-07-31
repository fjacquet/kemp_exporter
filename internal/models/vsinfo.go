package models

// VirtualServiceInfo is one entry from the listvs command. The stats payload
// omits service names, so this supplies them; the join key is address:port,
// never address alone — one VIP commonly hosts several ports.
type VirtualServiceInfo struct {
	Index    int    `xml:"Index"     json:"Index"`
	Name     string `xml:"NickName"  json:"NickName"`
	Address  string `xml:"VSAddress" json:"VSAddress"`
	Port     int    `xml:"VSPort"    json:"VSPort"`
	Protocol string `xml:"Protocol"  json:"Protocol"`
	Status   string `xml:"Status"    json:"Status"`
}
