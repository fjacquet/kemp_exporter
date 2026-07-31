package kemp

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/fjacquet/kemp_exporter/internal/config"
	"github.com/go-resty/resty/v2"
)

// xmlTransport speaks the classic LoadMaster RESTful API: a GET to
// /access/<cmd> with an apikey query parameter, answered with XML.
type xmlTransport struct {
	client *resty.Client
	apiKey string
}

// newXMLTransport builds the XML wire path for one system.
func newXMLTransport(sys config.System, trace bool) (*xmlTransport, error) {
	c, err := newRestyClient(sys, trace)
	if err != nil {
		return nil, err
	}
	return &xmlTransport{client: c, apiKey: sys.APIKey}, nil
}

// Name reports the wire encoding.
func (t *xmlTransport) Name() string { return "xml" }

// xmlEnvelope is the response wrapper every command shares. The payload is decoded
// separately (see decodeSuccessData) straight out of Success>Data into the caller's
// type.
type xmlEnvelope struct {
	XMLName xml.Name `xml:"Response"`
	Stat    string   `xml:"stat,attr"`
	Error   string   `xml:"Error"`
}

// Do issues cmd and decodes Success>Data into out.
func (t *xmlTransport) Do(ctx context.Context, cmd string, params map[string]string, out any) error {
	req := t.client.R().SetContext(ctx)
	if t.apiKey != "" {
		req.SetQueryParam("apikey", t.apiKey)
	}
	for k, v := range params {
		req.SetQueryParam(k, v)
	}
	resp, err := req.Get("/access/" + cmd)
	if err != nil {
		return fmt.Errorf("xml %s: %w", cmd, sanitizeTransportError(err))
	}
	switch {
	case resp.StatusCode() == http.StatusUnauthorized, resp.StatusCode() == http.StatusForbidden:
		return fmt.Errorf("xml %s: %w (status %d)", cmd, errAuth, resp.StatusCode())
	case resp.StatusCode() >= 400:
		return fmt.Errorf("xml %s: status %d", cmd, resp.StatusCode())
	}

	body := resp.Body()

	// Decode the envelope first so an API-level error is reported as such rather
	// than surfacing as an empty payload.
	var env xmlEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("xml %s: decode envelope: %w", cmd, err)
	}
	if env.Error != "" {
		return fmt.Errorf("xml %s: appliance error: %s", cmd, env.Error)
	}

	// Then decode the payload into the caller's type. encoding/xml cannot decode
	// into an `any` field holding a pointer via struct tags, so the Success>Data
	// element is located directly with a token loop and handed to out via
	// DecodeElement.
	if err := decodeSuccessData(body, out); err != nil {
		return fmt.Errorf("xml %s: decode payload: %w", cmd, err)
	}
	return nil
}

// decodeSuccessData walks body looking for the Data element nested under
// Response>Success, then decodes that element (and its children) into out.
//
// This does not call d.Skip() on non-matching elements: Skip() discards the entire
// subtree of the element whose start tag was just consumed, which would jump past
// Success and Data before ever finding them. Instead every token is inspected as the
// decoder naturally descends depth-first, and only the Data start element is handed
// off, via DecodeElement, once Success has been seen as an ancestor.
func decodeSuccessData(body []byte, out any) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	var inSuccess bool
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("locate Success>Data: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case start.Name.Local == "Success":
			inSuccess = true
		case inSuccess && start.Name.Local == "Data":
			return d.DecodeElement(out, &start)
		}
	}
}
