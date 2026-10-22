package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

type internetTraffic struct {
	Interface string
	Source    string
	Download  uint64
	Upload    uint64
}

func readInternetTraffic() (internetTraffic, error) {
	routes, err := os.Open("/proc/net/route")
	if err != nil {
		return internetTraffic{}, err
	}
	defer routes.Close()
	return internetTrafficSnapshot(routes, func(name string) bool {
		iface, err := net.InterfaceByName(name)
		return err == nil && iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0
	}, readUint64File)
}

func internetTrafficSnapshot(routes io.Reader, usable func(string) bool, read func(string) (uint64, error)) (internetTraffic, error) {
	names := make(map[string]bool)
	scanner := bufio.NewScanner(routes)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[0] == "lo" || fields[0] == tunIface {
			continue
		}
		dst, dstErr := strconv.ParseUint(fields[1], 16, 32)
		flags, flagErr := strconv.ParseUint(fields[3], 16, 32)
		mask, maskErr := strconv.ParseUint(fields[7], 16, 32)
		if dstErr != nil || flagErr != nil || maskErr != nil || dst != 0 || mask != 0 ||
			flags&0x1 == 0 || flags&0x200 != 0 || names[fields[0]] || !usable(fields[0]) {
			continue
		}
		names[fields[0]] = true
	}
	if err := scanner.Err(); err != nil {
		return internetTraffic{}, err
	}
	if len(names) == 0 {
		return internetTraffic{}, errors.New("no usable WAN default route")
	}
	interfaces := make([]string, 0, len(names))
	for name := range names {
		interfaces = append(interfaces, name)
	}
	sort.Strings(interfaces)
	result := internetTraffic{Interface: strings.Join(interfaces, ", "), Source: "wan:" + strings.Join(interfaces, ",")}
	// Only routed uplinks are counted: never add their underlying modem device
	// or Xray/speedtest payload counters to the same wire bytes a second time.
	for _, name := range interfaces {
		rx, err := read("/sys/class/net/" + name + "/statistics/rx_bytes")
		if err != nil {
			return internetTraffic{}, err
		}
		tx, err := read("/sys/class/net/" + name + "/statistics/tx_bytes")
		if err != nil {
			return internetTraffic{}, err
		}
		result.Download = addTrafficCounters(result.Download, rx)
		result.Upload = addTrafficCounters(result.Upload, tx)
	}
	return result, nil
}
