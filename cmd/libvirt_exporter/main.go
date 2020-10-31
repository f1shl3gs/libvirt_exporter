package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/NYTimes/gziphandler"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/f1shl3gs/libvirt_exporter/exporter"
)

func main() {
	var (
		listenAddress = flag.String("web.listen-address", ":5900", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		libvirtURI    = flag.String("libvirt.uri", "/var/run/libvirt/libvirt-sock", "Libvirt URI from which to extract metrics.")
		namespace     = flag.String("namespace", "libvirt", "Namespace for metrics")
		compress      = flag.Bool("web.gzip", true, "Enable gzip for http response")
	)
	flag.Parse()

	lc := exporter.NewExporter(*libvirtURI, exporter.WithNamespace(*namespace))

	prometheus.MustRegister(lc)
	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			<html>
			<head><title>Libvirt Exporter</title></head>
			<body>
			<h1>Libvirt Exporter</h1>
			<p><a href='` + *metricsPath + `'>Metrics</a></p>
			</body>
			</html>`))
	})

	var handler http.Handler = http.DefaultServeMux
	if *compress {
		handler = gziphandler.GzipHandler(http.DefaultServeMux)
	}

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Printf("listen to %s failed, %s\n", *listenAddress, err)
		os.Exit(1)
	}

	log.Printf("Libvirt exporter started, listening at %s\n", *listenAddress)
	if err = http.Serve(listener, handler); err != nil {
		log.Printf("http serve failed, %s\n", err)
		os.Exit(1)
	}
}
