package main

import (
	"strings"
	"testing"
	"time"
)

const testHTML = `<!DOCTYPE html>
<html lang="en">
<head><title>Purple I2P Webconsole</title></head>
<body>
<div class="content">

<b>Uptime:</b> 1 day, 1 hours, 1 minutes, 1 seconds<br>
<b>Network status:</b> OK<br>
<b>Network status v6:</b> OK<br>
<b>Tunnel creation success rate:</b> 4%<br>
<b>Received:</b> 100.1 GiB (3301.31 KiB/s)<br>
<b>Sent:</b> 100.2 GiB (3300.43 KiB/s)<br>
<b>Transit:</b> 98 GiB (3000.55 KiB/s)<br>
<b>Data path:</b> /home/i2pd/data<br>
<b>Router Ident:</b>redacted<br>
<b>Router Caps:</b> PR<br>
<b>Version:</b>2.59.0<br>
<b>Routers:</b> 10100&nbsp;&nbsp;&nbsp;<b>Floodfills:</b> 3612&nbsp;&nbsp;&nbsp;<b>LeaseSets:</b> 0<br>
<b>Client Tunnels:</b> 10&nbsp;&nbsp;&nbsp;<b>Transit Tunnels:</b> 1234<br>

<table class="services">
<caption>Services</caption>
<tbody>
<tr><td>HTTP Proxy</td><td class='enabled'>Enabled</td></tr>
<tr><td>SOCKS Proxy</td><td class='enabled'>Enabled</td></tr>
<tr><td>BOB</td><td class='disabled'>Disabled</td></tr>
<tr><td>SAM</td><td class='enabled'>Enabled</td></tr>
<tr><td>I2CP</td><td class='disabled'>Disabled</td></tr>
<tr><td>I2PControl</td><td class='disabled'>Disabled</td></tr>
</tbody>
</table>

</div>
</body>
</html>`

func TestCollectMetrics(t *testing.T) {
	output := collectMetrics(testHTML, 42*time.Millisecond)

	expected := map[string]string{
		"i2pd_up":              "1",
		"i2pd_uptime_seconds":  "90061",                        // 1*86400 + 1*3600 + 1*60 + 1
		"i2pd_routers":         "10100",
		"i2pd_floodfills":      "3612",
		"i2pd_leasesets":       "0",
		"i2pd_client_tunnels":  "10",
		"i2pd_transit_tunnels": "1234",
		"i2pd_tunnel_creation_success_rate_percent": "4",
	}

	for metric, val := range expected {
		line := metric + " " + val
		if !strings.Contains(output, line) {
			t.Errorf("expected metric line %q not found in output:\n%s", line, output)
		}
	}

	expectedLabeled := []string{
		`i2pd_network_status{protocol="v4"} 1`,
		`i2pd_network_status{protocol="v6"} 1`,
		`i2pd_traffic_bytes_total{direction="received"}`,
		`i2pd_traffic_bytes_total{direction="sent"}`,
		`i2pd_traffic_bytes_total{direction="transit"}`,
		`i2pd_traffic_bytes_per_second{direction="received"}`,
		`i2pd_traffic_bytes_per_second{direction="sent"}`,
		`i2pd_traffic_bytes_per_second{direction="transit"}`,
		`i2pd_version_info{version="2.59.0"} 1`,
		`i2pd_router_caps_info{caps="PR"} 1`,
		`i2pd_service_enabled{service="http_proxy"} 1`,
		`i2pd_service_enabled{service="socks_proxy"} 1`,
		`i2pd_service_enabled{service="bob"} 0`,
		`i2pd_service_enabled{service="sam"} 1`,
		`i2pd_service_enabled{service="i2cp"} 0`,
		`i2pd_service_enabled{service="i2pcontrol"} 0`,
	}

	for _, line := range expectedLabeled {
		if !strings.Contains(output, line) {
			t.Errorf("expected metric line %q not found in output:\n%s", line, output)
		}
	}
}

func TestUptimeParsing(t *testing.T) {
	cases := []struct {
		html string
		want string
	}{
		{`<b>Uptime:</b> 0 days, 0 hours, 5 minutes, 30 seconds<br>`, "i2pd_uptime_seconds 330"},
		{`<b>Uptime:</b> 2 days, 12 hours, 0 minutes, 0 seconds<br>`, "i2pd_uptime_seconds 216000"},
		{`<b>Uptime:</b> 0 days, 0 hours, 0 minutes, 1 seconds<br>`, "i2pd_uptime_seconds 1"},
	}
	for _, tc := range cases {
		out := collectMetrics(tc.html, 0)
		if !strings.Contains(out, tc.want) {
			t.Errorf("for %q: expected %q in output:\n%s", tc.html, tc.want, out)
		}
	}
}

func TestTrafficParsing(t *testing.T) {
	html := `<b>Received:</b> 1 GiB (1024 KiB/s)<br>`
	out := collectMetrics(html, 0)

	// 1 GiB = 1073741824 bytes
	if !strings.Contains(out, `i2pd_traffic_bytes_total{direction="received"} 1073741824`) {
		t.Errorf("unexpected traffic total in output:\n%s", out)
	}
	// 1024 KiB/s = 1048576 bytes/s
	if !strings.Contains(out, `i2pd_traffic_bytes_per_second{direction="received"} 1048576`) {
		t.Errorf("unexpected traffic rate in output:\n%s", out)
	}
}

func TestSanitizeLabel(t *testing.T) {
	cases := map[string]string{
		"HTTP Proxy": "http_proxy",
		"SOCKS Proxy": "socks_proxy",
		"BOB":         "bob",
		"SAM":         "sam",
		"I2CP":        "i2cp",
		"I2PControl":  "i2pcontrol",
	}
	for input, want := range cases {
		got := sanitizeLabel(input)
		if got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFmtFloat(t *testing.T) {
	cases := []struct {
		val  float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{1.5, "1.5"},
		{100.123, "100.123"},
		{1073741824, "1073741824"},
	}
	for _, tc := range cases {
		got := fmtFloat(tc.val)
		if got != tc.want {
			t.Errorf("fmtFloat(%v) = %q, want %q", tc.val, got, tc.want)
		}
	}
}
