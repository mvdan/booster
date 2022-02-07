package main

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

/*func TestHwPath(t *testing.T) {
	check := func(input, expected string) {
		output, err := hwPath(input)
		require.NoError(t, err)
		require.Equal(t, expected, output)
	}

	check("/sys/devices/pci0000:00/0000:00:17.0/ata3/host2/target2:0:0/2:0:0:0/block/sda", "pci-0000:00:17.0-ata-3.0") // or pci-0000:00:17.0-ata-3.0 ?
	check("/sys/devices/pci0000:00/0000:00:1d.0/0000:02:00.0/nvme/nvme1/nvme1n1", "pci-0000:02:00.0-nvme-1")
	check("/sys/devices/pci0000:00/0000:00:1b.0/0000:03:00.0/nvme/nvme0/nvme0n1", "pci-0000:03:00.0-nvme-1")
}*/

// Goes over host's block devices and verifies its hw path
func TestHostHwPath(t *testing.T) {
	ents, err := os.ReadDir("/sys/block")
	require.NoError(t, err)
	var generatedPaths []string
	for _, e := range ents {
		path, err := hwPath("/dev/" + e.Name())
		require.NoError(t, err)
		if path == "" {
			continue // the block has no hardware path
		}
		generatedPaths = append(generatedPaths, path)
	}

	ents, err = os.ReadDir("/dev/disk/by-path")
	require.NoError(t, err)
	var existedPaths []string

	partitionRe, err := regexp.Compile(`-part\d+$`)
	require.NoError(t, err)

	compatAtaRe, err := regexp.Compile(`-ata-\d+$`)
	require.NoError(t, err)

	for _, e := range ents {
		if partitionRe.MatchString(e.Name()) {
			// ignore partitions
			continue
		}
		if compatAtaRe.MatchString(e.Name()) {
			// booster does not support compat ATA paths
			continue
		}

		existedPaths = append(existedPaths, e.Name())
	}

	sort.Strings(existedPaths)
	sort.Strings(generatedPaths)
	require.Equal(t, existedPaths, generatedPaths)
}
