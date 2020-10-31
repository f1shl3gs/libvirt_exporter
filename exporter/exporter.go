package exporter

import (
	"encoding/hex"
	"encoding/xml"
	"log"
	"net"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	domainStates = []string{
		"nostate",
		"running",
		"blocked",
		"paused",
		"shutdown",
		"shutoff",
		"crashed",
		"pmsuspended",
		"last",
	}
)

type Exporter struct {
	uri       string
	namespace string

	// misc
	up            *prometheus.Desc
	domains       *prometheus.Desc
	scrapeError   *prometheus.Desc
	scrapeLatency *prometheus.Desc

	// instance
	state   *prometheus.Desc
	maxMem  *prometheus.Desc
	mem     *prometheus.Desc
	vcpu    *prometheus.Desc
	cputime *prometheus.Desc

	// memory stats
	rss *prometheus.Desc

	// block
	blockReadBytes  *prometheus.Desc
	blockReadReqs   *prometheus.Desc
	blockWriteBytes *prometheus.Desc
	blockWriteReqs  *prometheus.Desc

	// interfaces
	ifaceReceiveBytes    *prometheus.Desc
	ifaceReceivePackets  *prometheus.Desc
	ifaceReceiveErrors   *prometheus.Desc
	ifaceReceiveDrops    *prometheus.Desc
	ifaceTransmitBytes   *prometheus.Desc
	ifaceTransmitPackets *prometheus.Desc
	ifaceTransmitErrors  *prometheus.Desc
	ifaceTransmitDrops   *prometheus.Desc
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	// misc
	ch <- e.up
	ch <- e.domains
	ch <- e.scrapeError
	ch <- e.scrapeLatency

	// instance
	ch <- e.state
	ch <- e.maxMem
	ch <- e.mem
	ch <- e.vcpu
	ch <- e.cputime

	// block
	ch <- e.blockReadReqs
	ch <- e.blockReadBytes
	ch <- e.blockWriteReqs
	ch <- e.blockWriteBytes

	// iface
	ch <- e.ifaceReceiveBytes
	ch <- e.ifaceReceivePackets
	ch <- e.ifaceReceiveErrors
	ch <- e.ifaceReceiveDrops
	ch <- e.ifaceTransmitBytes
	ch <- e.ifaceTransmitPackets
	ch <- e.ifaceTransmitErrors
	ch <- e.ifaceTransmitDrops
}

func (e *Exporter) Collect(metrics chan<- prometheus.Metric) {
	var (
		scrapeError = 0.0
		start       = time.Now()
	)

	if err := e.collect(metrics); err != nil {
		scrapeError = 1.0
		log.Printf("collect metrics failed, %s\n", err)
	}

	latency := time.Since(start)
	metrics <- prometheus.MustNewConstMetric(
		e.scrapeLatency,
		prometheus.GaugeValue,
		latency.Seconds())

	metrics <- prometheus.MustNewConstMetric(
		e.scrapeError,
		prometheus.GaugeValue,
		scrapeError,
	)
}

func (e *Exporter) collect(metrics chan<- prometheus.Metric) error {
	conn, err := net.DialTimeout("unix", e.uri, 5*time.Second)
	if err != nil {
		return err
	}

	defer conn.Close()

	cli := libvirt.New(conn)
	if err = cli.Connect(); err != nil {
		return errors.Wrap(err, "failed to connect")
	}

	defer cli.Disconnect()

	// todo: always 1.0!?
	metrics <- prometheus.MustNewConstMetric(
		e.up,
		prometheus.GaugeValue,
		1.0)

	domains, err := cli.Domains()
	if err != nil {
		return errors.Wrap(err, "failed to load domain")
	}

	//domains number
	domainNumber := len(domains)
	metrics <- prometheus.MustNewConstMetric(
		e.domains,
		prometheus.GaugeValue,
		float64(domainNumber))

	for _, domain := range domains {
		err = e.collectDomain(metrics, cli, domain)
		if err != nil {
			return errors.Wrap(err, "failed to collect domain")
		}
	}
	return nil
}

func encodeHex(dst []byte, uuid libvirt.UUID) {
	hex.Encode(dst, uuid[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], uuid[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], uuid[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], uuid[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], uuid[10:])
}

func uuidConvert(uuid libvirt.UUID) string {
	var buf [36]byte
	encodeHex(buf[:], uuid)
	return string(buf[:])
}

func (e *Exporter) collectDomain(ch chan<- prometheus.Metric, cli *libvirt.Libvirt, domain libvirt.Domain) error {
	xmlDesc, err := cli.DomainGetXMLDesc(domain, 0)
	if err != nil {
		return errors.Wrap(err, "failed to DomainGetXMLDesc")
	}

	var libvirtSchema Domain
	err = xml.Unmarshal([]byte(xmlDesc), &libvirtSchema)
	if err != nil {
		return errors.Wrap(err, "failed to unmarshal domain")
	}

	name := domain.Name
	uuid := uuidConvert(domain.UUID)

	state, maxMem, mem, vcpu, cputime, err := cli.DomainGetInfo(domain)
	if err != nil {
		return errors.Wrap(err, "failed to get domain info")
	}

	// same as `virsh dommemstat xxx`
	// actual 8388608
	// last_update 0
	// rss 2897276
	stats, err := cli.DomainMemoryStats(domain, 8, 0)
	if err != nil {
		return errors.Wrap(err, "DomainMemoryStats failed")
	}

	for i := 0; i < len(stats); i++ {
		if stats[i].Tag == int32(libvirt.DomainMemoryStatRss) {
			ch <- prometheus.MustNewConstMetric(
				e.rss,
				prometheus.GaugeValue,
				float64(stats[i].Val*1024),
				name, uuid)
		}
	}

	ch <- prometheus.MustNewConstMetric(
		e.state,
		prometheus.GaugeValue,
		float64(state),
		name, uuid, domainStates[state])

	ch <- prometheus.MustNewConstMetric(
		e.maxMem,
		prometheus.GaugeValue,
		float64(maxMem)*1024,
		name, uuid)
	ch <- prometheus.MustNewConstMetric(
		e.mem,
		prometheus.GaugeValue,
		float64(mem)*1024,
		name, uuid)
	ch <- prometheus.MustNewConstMetric(
		e.vcpu,
		prometheus.GaugeValue,
		float64(vcpu),
		name, uuid)
	ch <- prometheus.MustNewConstMetric(
		e.cputime,
		prometheus.CounterValue,
		float64(cputime)/1e9,
		name, uuid)

	// Report block device statistics.
	for _, disk := range libvirtSchema.Devices.Disks {
		if disk.Device == "cdrom" || disk.Device == "fd" {
			continue
		}

		isActive, err := cli.DomainIsActive(domain)
		var rRdReq, rRdBytes, rWrReq, rWrBytes int64
		if isActive == 1 {
			rRdReq, rRdBytes, rWrReq, rWrBytes, _, err = cli.DomainBlockStats(domain, disk.Target.Device)
		}

		if err != nil {
			return errors.Wrap(err, "failed to get DomainBlockStats")
		}

		ch <- prometheus.MustNewConstMetric(
			e.blockReadBytes,
			prometheus.CounterValue,
			float64(rRdBytes),
			name, uuid,
			disk.Source.File,
			disk.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.blockReadReqs,
			prometheus.CounterValue,
			float64(rRdReq),
			name, uuid,
			disk.Source.File,
			disk.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.blockWriteBytes,
			prometheus.CounterValue,
			float64(rWrBytes),
			name, uuid,
			disk.Source.File,
			disk.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.blockWriteReqs,
			prometheus.CounterValue,
			float64(rWrReq),
			name, uuid,
			disk.Source.File,
			disk.Target.Device)
	}

	// Report network interface statistics.
	for _, iface := range libvirtSchema.Devices.Interfaces {
		if iface.Target.Device == "" {
			continue
		}
		isActive, err := cli.DomainIsActive(domain)
		var rRxBytes, rRxPackets, rRxErrs, rRxDrop, rTxBytes, rTxPackets, rTxErrs, rTxDrop int64
		if isActive == 1 {
			rRxBytes, rRxPackets, rRxErrs, rRxDrop, rTxBytes, rTxPackets, rTxErrs, rTxDrop, err = cli.DomainInterfaceStats(domain, iface.Target.Device)
		}

		if err != nil {
			return errors.Wrap(err, "failed to get DomainInterfaceStats")
		}

		ch <- prometheus.MustNewConstMetric(
			e.ifaceReceiveBytes,
			prometheus.CounterValue,
			float64(rRxBytes),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceReceivePackets,
			prometheus.CounterValue,
			float64(rRxPackets),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceReceiveErrors,
			prometheus.CounterValue,
			float64(rRxErrs),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceReceiveDrops,
			prometheus.CounterValue,
			float64(rRxDrop),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceTransmitBytes,
			prometheus.CounterValue,
			float64(rTxBytes),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceTransmitPackets,
			prometheus.CounterValue,
			float64(rTxPackets),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceTransmitErrors,
			prometheus.CounterValue,
			float64(rTxErrs),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)

		ch <- prometheus.MustNewConstMetric(
			e.ifaceTransmitDrops,
			prometheus.CounterValue,
			float64(rTxDrop),
			name, uuid,
			iface.Source.Bridge,
			iface.Target.Device)
	}

	return nil
}

type Option func(exporter *Exporter)

func WithNamespace(ns string) Option {
	return func(e *Exporter) {
		e.namespace = ns
	}
}

func NewExporter(uri string, opts ...Option) *Exporter {
	e := &Exporter{
		namespace: "libvirt",
		uri:       uri,
	}

	for _, h := range opts {
		h(e)
	}

	// descs
	e.up = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "", "up"),
		"Whether scraping libvirt's metrics was successful.",
		nil,
		nil)
	e.domains = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "", "domains_total"),
		"Number of the domain",
		nil,
		nil)
	e.scrapeError = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "", "scrape_error"),
		"Scrape status of libvirt",
		nil,
		nil)
	e.scrapeLatency = prometheus.NewDesc(
		"libvirt_scrape_latency",
		"Scrape latency in second",
		nil, nil)

	e.state = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "", "domain_state"),
		"Code of the domain state",
		[]string{"domain", "uuid", "state"},
		nil)
	e.maxMem = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_info", "maximum_memory_bytes"),
		"Maximum allowed memory of the domain, in bytes.",
		[]string{"domain", "uuid"},
		nil)
	e.mem = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_info", "memory_usage_bytes"),
		"Memory usage of the domain, in bytes.",
		[]string{"domain", "uuid"},
		nil)
	e.vcpu = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_info", "virtual_cpus"),
		"Number of virtual CPUs for the domain.",
		[]string{"domain", "uuid"},
		nil)
	e.cputime = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_info", "cpu_time_seconds_total"),
		"Amount of CPU time used by the domain, in seconds.",
		[]string{"domain", "uuid"},
		nil)
	e.rss = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_info", "memory_rss_bytes"),
		"A mount memory of the instance",
		[]string{"domain", "uuid"},
		nil)

	// block
	e.blockReadBytes = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_block", "read_bytes_total"),
		"Number of bytes read from a block device, in bytes.",
		[]string{"domain", "uuid", "source_file", "target_device"},
		nil)
	e.blockReadReqs = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_block", "read_requests_total"),
		"Number of read requests from a block device.",
		[]string{"domain", "uuid", "source_file", "target_device"},
		nil)
	e.blockWriteBytes = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_block", "write_bytes_total"),
		"Number of bytes write from a block device, in bytes.",
		[]string{"domain", "uuid", "source_file", "target_device"},
		nil)
	e.blockWriteReqs = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_block", "write_requests_total"),
		"Number of write requests from a block device.",
		[]string{"domain", "uuid", "source_file", "target_device"},
		nil)

	// iface
	e.ifaceReceiveBytes = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "receive_bytes_total"),
		"Number of bytes received on a network interface, in bytes.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceReceivePackets = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "receive_packets_total"),
		"Number of packets received on a network interface.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceReceiveErrors = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "receive_errors_total"),
		"Number of packet receive errors on a network interface.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceReceiveDrops = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "receive_drops_total"),
		"Number of packet receive drops on a network interface.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceTransmitBytes = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "transmit_bytes_total"),
		"Number of bytes transmitted on a network interface, in bytes.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceTransmitPackets = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "transmit_packets_total"),
		"Number of packets transmitted on a network interface.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceTransmitErrors = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "transmit_errors_total"),
		"Number of packet transmit errors on a network interface.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)
	e.ifaceTransmitDrops = prometheus.NewDesc(
		prometheus.BuildFQName(e.namespace, "domain_interface", "transmit_drops_total"),
		"Number of packet transmit drops on a network interface.",
		[]string{"domain", "uuid", "source_bridge", "target_device"},
		nil)

	return e
}
