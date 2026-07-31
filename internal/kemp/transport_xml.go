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
	if err := checkEnvelopeStat(cmd, env.Stat); err != nil {
		return err
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

// checkEnvelopeStat maps a non-"200" Response stat attribute to an actionable
// error.
//
// The appliance can report a rejected credential purely through this attribute,
// with no <Error> child element at all -- e.g. HTTP 200 with body
// `<Response stat="401"><Success/></Response>`. Left unchecked, that shape falls
// straight through to decodeSuccessData, which reports "locate Success>Data:
// EOF" -- indistinguishable from a genuinely truncated response, and not
// errors.Is(err, errAuth)-matchable by a caller that needs to know not to retry.
func checkEnvelopeStat(cmd, stat string) error {
	switch stat {
	case "", "200":
		return nil
	case "401", "403":
		return fmt.Errorf("xml %s: %w (stat %s)", cmd, errAuth, stat)
	default:
		return fmt.Errorf("xml %s: appliance returned stat %s", cmd, stat)
	}
}

// decodeSuccessData walks body looking for the Data element that is a direct
// child of Response>Success, then decodes that element (and its children) into
// out.
//
// This does not call d.Skip() on non-matching elements: Skip() discards the entire
// subtree of the element whose start tag was just consumed, which would jump past
// Success and Data before ever finding them. Instead every token is inspected as the
// decoder naturally descends depth-first, and only the Data start element is handed
// off, via DecodeElement, once Success has been seen as its immediate parent.
//
// Depth is tracked explicitly so a Data nested further down (Success>Wrap>Data) is
// not mistaken for the payload, and successDepth is cleared on Success's matching
// EndElement so a Data that is a sibling of Success, not a child of it, is not
// mistaken for the payload either.
func decodeSuccessData(body []byte, out any) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	const notInSuccess = -1
	successDepth := notInSuccess
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("locate Success>Data: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case t.Name.Local == "Success" && successDepth == notInSuccess:
				successDepth = depth
			case t.Name.Local == "Data" && successDepth != notInSuccess && depth == successDepth+1:
				return d.DecodeElement(out, &t)
			}
		case xml.EndElement:
			if successDepth != notInSuccess && depth == successDepth {
				successDepth = notInSuccess
			}
			depth--
		}
	}
}
