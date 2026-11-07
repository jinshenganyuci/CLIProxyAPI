package traffic

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readNetworkSample(iface string) (NetworkSample, error) {
	if iface == "" || strings.ContainsAny(iface, "/\\\x00\n") {
		return NetworkSample{}, fmt.Errorf("traffic: invalid interface name")
	}
	raw, errRead := os.ReadFile("/proc/net/dev")
	if errRead != nil {
		return NetworkSample{}, fmt.Errorf("traffic network counters unavailable: %w", errRead)
	}
	rx, tx, errParse := parseNetworkCounters(string(raw), iface)
	if errParse != nil {
		return NetworkSample{}, errParse
	}
	boot, errBoot := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if errBoot != nil {
		return NetworkSample{}, errBoot
	}
	namespace, errNamespace := os.Readlink("/proc/self/ns/net")
	if errNamespace != nil {
		return NetworkSample{}, errNamespace
	}
	index, errIndex := os.ReadFile(filepath.Join("/sys/class/net", iface, "ifindex"))
	if errIndex != nil {
		return NetworkSample{}, errIndex
	}
	address, errAddress := os.ReadFile(filepath.Join("/sys/class/net", iface, "address"))
	if errAddress != nil {
		return NetworkSample{}, errAddress
	}
	epoch := strings.TrimSpace(string(boot)) + "|" + namespace + "|" + iface + "|" + strings.TrimSpace(string(index)) + "|" + strings.TrimSpace(string(address))
	return NetworkSample{Epoch: epoch, RxBytes: rx, TxBytes: tx}, nil
}

func parseNetworkCounters(raw, iface string) (uint64, uint64, error) {
	for _, line := range strings.Split(raw, "\n") {
		name, values, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != iface {
			continue
		}
		fields := strings.Fields(values)
		if len(fields) < 16 {
			return 0, 0, fmt.Errorf("traffic: truncated interface counters")
		}
		rx, errRx := strconv.ParseUint(fields[0], 10, 64)
		if errRx != nil {
			return 0, 0, fmt.Errorf("traffic: invalid receive counter")
		}
		tx, errTx := strconv.ParseUint(fields[8], 10, 64)
		if errTx != nil {
			return 0, 0, fmt.Errorf("traffic: invalid transmit counter")
		}
		return rx, tx, nil
	}
	return 0, 0, fmt.Errorf("traffic: interface %s unavailable", iface)
}

// SampleNetwork records one interface delta. The first reading or a reset only
// establishes a baseline; it never subtracts from historical totals.
func (c *Collector) SampleNetwork() error {
	if c == nil {
		return nil
	}
	c.sampleMu.Lock()
	defer c.sampleMu.Unlock()
	if !c.enabled.Load() {
		return nil
	}
	c.mu.RLock()
	iface := c.iface
	c.mu.RUnlock()
	sample, errSample := c.readNetwork(iface)
	if errSample != nil {
		c.mu.Lock()
		c.networkError = errSample.Error()
		c.mu.Unlock()
		return errSample
	}
	if sample.Epoch == "" {
		return fmt.Errorf("traffic: empty network epoch")
	}
	now := c.now()
	if c.hasBaseline && c.baseline.Epoch == sample.Epoch && sample.RxBytes >= c.baseline.RxBytes && sample.TxBytes >= c.baseline.TxBytes {
		c.network.add(dayNumber(now), 0, sample.RxBytes-c.baseline.RxBytes, sample.TxBytes-c.baseline.TxBytes)
	}
	c.baseline = sample
	c.hasBaseline = true
	c.mu.Lock()
	stamp := now.UTC()
	c.lastSampled = &stamp
	c.networkError = ""
	c.mu.Unlock()
	return nil
}
