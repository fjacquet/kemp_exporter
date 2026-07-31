package kemp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/fjacquet/kemp_exporter/internal/models"
)

// The two wire paths must decode to an identical Statistics. This is the invariant
// that lets every layer above the transport ignore which encoding was used; without
// it the single-model design silently produces different metrics per firmware.
func TestTransportParityStatistics(t *testing.T) {
	xmlSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBytes(w, fixture(t, "stats.xml"))
	}))
	defer xmlSrv.Close()

	jsonSrv, _, _ := jsonServer(t, fixture(t, "stats.json"), false)
	defer jsonSrv.Close()

	xt, err := newXMLTransport(systemFor(t, xmlSrv), false)
	if err != nil {
		t.Fatalf("newXMLTransport: %v", err)
	}
	jt, err := newJSONTransport(jsonSystem(t, jsonSrv), false)
	if err != nil {
		t.Fatalf("newJSONTransport: %v", err)
	}

	var fromXML, fromJSON models.Statistics
	if err := xt.Do(context.Background(), "stats", nil, &fromXML); err != nil {
		t.Fatalf("xml Do: %v", err)
	}
	if err := jt.Do(context.Background(), "stats", nil, &fromJSON); err != nil {
		t.Fatalf("json Do: %v", err)
	}

	if !reflect.DeepEqual(fromXML, fromJSON) {
		t.Errorf("transports decoded to different Statistics.\nxml:  %+v\njson: %+v", fromXML, fromJSON)
	}
}
