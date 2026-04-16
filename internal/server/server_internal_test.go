package server

import (
	"net/http"
	"net/url"
	"testing"
)

func TestRouteLabelPrefersPatternPath(t *testing.T) {
	req := &http.Request{
		Pattern: "GET /config/{clientID}",
		URL:     &url.URL{Path: "/config/device-123"},
	}

	if got := routeLabel(req); got != "/config/{clientID}" {
		t.Fatalf("routeLabel: want /config/{clientID} got %q", got)
	}
}

func TestRouteLabelFallsBackToURLPath(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{Path: "/metrics"},
	}

	if got := routeLabel(req); got != "/metrics" {
		t.Fatalf("routeLabel: want /metrics got %q", got)
	}
}
