package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reUptime      = regexp.MustCompile(`(?i)<b>Uptime:</b>\s*(.+?)<br`)
	reUptimePart  = regexp.MustCompile(`(\d+)\s+(day|hour|minute|second)`)
	reNetStatus   = regexp.MustCompile(`(?i)<b>Network status:</b>\s*(\w+)`)
	reNetStatusV6 = regexp.MustCompile(`(?i)<b>Network status v6:</b>\s*(\w+)`)
	reTunnelRate  = regexp.MustCompile(`(?i)<b>Tunnel creation success rate:</b>\s*([\d.]+)\s*%`)
	reTraffic     = regexp.MustCompile(`(?i)<b>(Received|Sent|Transit):</b>\s*([\d.]+)\s*(\w+)\s*\(([\d.]+)\s*(\w+/s)\)`)
	reRouters     = regexp.MustCompile(`(?i)<b>Routers:</b>\s*(\d+)`)
	reFloodfills  = regexp.MustCompile(`(?i)<b>Floodfills:</b>\s*(\d+)`)
	reLeaseSets   = regexp.MustCompile(`(?i)<b>LeaseSets:</b>\s*(\d+)`)
	reClientTun   = regexp.MustCompile(`(?i)<b>Client Tunnels:</b>\s*(\d+)`)
	reTransitTun  = regexp.MustCompile(`(?i)<b>Transit Tunnels:</b>\s*(\d+)`)
	reVersion     = regexp.MustCompile(`(?i)<b>Version:</b>\s*([\d.]+)`)
	reCaps        = regexp.MustCompile(`(?i)<b>Router Caps:</b>\s*(\w+)`)
	reService     = regexp.MustCompile(`<tr><td>([^<]+)</td><td\s+class='(enabled|disabled)'`)
)

func main() {
	listenAddr := flag.String("listen", ":9101", "address to listen on for metrics")
	i2pdURL := flag.String("url", "http://127.0.0.1:7070", "i2pd web console URL")
	timeout := flag.Duration("timeout", 5*time.Second, "HTTP client timeout")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		serveMetrics(w, client, *i2pdURL)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>i2pd Exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`)
	})

	log.Printf("i2pd exporter listening on %s, scraping %s", *listenAddr, *i2pdURL)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}

func serveMetrics(w http.ResponseWriter, client *http.Client, url string) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	start := time.Now()

	resp, err := client.Get(url)
	if err != nil {
		log.Printf("scrape error: %v", err)
		writeDown(w, time.Since(start))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("i2pd returned status %d", resp.StatusCode)
		writeDown(w, time.Since(start))
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("read error: %v", err)
		writeDown(w, time.Since(start))
		return
	}

	duration := time.Since(start)
	fmt.Fprint(w, collectMetrics(string(body), duration))
}

func writeDown(w http.ResponseWriter, duration time.Duration) {
	fmt.Fprint(w, "# HELP i2pd_up Whether the i2pd console is reachable\n")
	fmt.Fprint(w, "# TYPE i2pd_up gauge\n")
	fmt.Fprint(w, "i2pd_up 0\n")
	fmt.Fprintf(w, "# HELP i2pd_scrape_duration_seconds Time spent scraping i2pd console\n")
	fmt.Fprintf(w, "# TYPE i2pd_scrape_duration_seconds gauge\n")
	fmt.Fprintf(w, "i2pd_scrape_duration_seconds %s\n", fmtFloat(duration.Seconds()))
}

func collectMetrics(html string, scrapeDuration time.Duration) string {
	w := &promWriter{seen: make(map[string]bool)}

	w.gauge("i2pd_up", "Whether the i2pd console is reachable", 1)
	w.gauge("i2pd_scrape_duration_seconds", "Time spent scraping i2pd console", scrapeDuration.Seconds())

	// Uptime
	if m := reUptime.FindStringSubmatch(html); m != nil {
		var secs float64
		for _, p := range reUptimePart.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.ParseFloat(p[1], 64)
			switch {
			case strings.HasPrefix(p[2], "day"):
				secs += n * 86400
			case strings.HasPrefix(p[2], "hour"):
				secs += n * 3600
			case strings.HasPrefix(p[2], "minute"):
				secs += n * 60
			case strings.HasPrefix(p[2], "second"):
				secs += n
			}
		}
		w.gauge("i2pd_uptime_seconds", "Router uptime in seconds", secs)
	}

	// Network status
	if m := reNetStatus.FindStringSubmatch(html); m != nil {
		w.gaugeL("i2pd_network_status", "Network status (1=OK, 0=other)",
			bval(m[1] == "OK"), "protocol", "v4")
	}
	if m := reNetStatusV6.FindStringSubmatch(html); m != nil {
		w.gaugeL("i2pd_network_status", "",
			bval(m[1] == "OK"), "protocol", "v6")
	}

	// Tunnel creation success rate
	if m := reTunnelRate.FindStringSubmatch(html); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		w.gauge("i2pd_tunnel_creation_success_rate_percent", "Tunnel creation success rate", v)
	}

	// Traffic (received, sent, transit)
	for _, m := range reTraffic.FindAllStringSubmatch(html, -1) {
		dir := strings.ToLower(m[1])
		total, _ := strconv.ParseFloat(m[2], 64)
		total *= unitBytes(m[3])
		rate, _ := strconv.ParseFloat(m[4], 64)
		rate *= unitBytes(strings.TrimSuffix(m[5], "/s"))

		w.counterL("i2pd_traffic_bytes_total", "Total traffic in bytes",
			total, "direction", dir)
		w.gaugeL("i2pd_traffic_bytes_per_second", "Traffic rate in bytes per second",
			rate, "direction", dir)
	}

	// Router database
	if m := reRouters.FindStringSubmatch(html); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		w.gauge("i2pd_routers", "Number of known routers", v)
	}
	if m := reFloodfills.FindStringSubmatch(html); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		w.gauge("i2pd_floodfills", "Number of known floodfills", v)
	}
	if m := reLeaseSets.FindStringSubmatch(html); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		w.gauge("i2pd_leasesets", "Number of known lease sets", v)
	}

	// Tunnels
	if m := reClientTun.FindStringSubmatch(html); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		w.gauge("i2pd_client_tunnels", "Number of client tunnels", v)
	}
	if m := reTransitTun.FindStringSubmatch(html); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		w.gauge("i2pd_transit_tunnels", "Number of transit tunnels", v)
	}

	// Info metrics
	if m := reVersion.FindStringSubmatch(html); m != nil {
		w.gaugeL("i2pd_version_info", "i2pd version", 1, "version", m[1])
	}
	if m := reCaps.FindStringSubmatch(html); m != nil {
		w.gaugeL("i2pd_router_caps_info", "Router capability flags", 1, "caps", m[1])
	}

	// Services
	for _, m := range reService.FindAllStringSubmatch(html, -1) {
		name := sanitizeLabel(m[1])
		w.gaugeL("i2pd_service_enabled", "Whether a service is enabled (1=yes, 0=no)",
			bval(m[2] == "enabled"), "service", name)
	}

	return w.String()
}

// promWriter emits Prometheus text exposition format.
type promWriter struct {
	b    strings.Builder
	seen map[string]bool
}

func (w *promWriter) gauge(name, help string, value float64) {
	w.emit(name, "gauge", help, value, "")
}

func (w *promWriter) gaugeL(name, help string, value float64, lk, lv string) {
	w.emit(name, "gauge", help, value, fmt.Sprintf(`%s="%s"`, lk, lv))
}

func (w *promWriter) counterL(name, help string, value float64, lk, lv string) {
	w.emit(name, "counter", help, value, fmt.Sprintf(`%s="%s"`, lk, lv))
}

func (w *promWriter) emit(name, typ, help string, value float64, labelExpr string) {
	if !w.seen[name] {
		if help != "" {
			fmt.Fprintf(&w.b, "# HELP %s %s\n", name, help)
		}
		fmt.Fprintf(&w.b, "# TYPE %s %s\n", name, typ)
		w.seen[name] = true
	}
	if labelExpr != "" {
		fmt.Fprintf(&w.b, "%s{%s} %s\n", name, labelExpr, fmtFloat(value))
	} else {
		fmt.Fprintf(&w.b, "%s %s\n", name, fmtFloat(value))
	}
}

func (w *promWriter) String() string {
	return w.b.String()
}

func fmtFloat(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) && !math.IsNaN(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func unitBytes(unit string) float64 {
	switch strings.TrimSpace(unit) {
	case "KiB":
		return 1024
	case "MiB":
		return 1 << 20
	case "GiB":
		return 1 << 30
	case "TiB":
		return 1 << 40
	default:
		return 1
	}
}

func bval(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func sanitizeLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}
