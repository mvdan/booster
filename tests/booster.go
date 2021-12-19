package tests

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"tests/israce"
	"time"

	"github.com/anatol/vmtest"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var (
	binariesDir string // working dir shared between all tests
)

func generateInitRamfs(workDir string, opts Opts) (string, error) {
	output := path.Join(workDir, "booster.img")
	config := path.Join(workDir, "config.yaml")

	if err := generateBoosterConfig(config, opts); err != nil {
		return "", err
	}

	generatorArgs := []string{"build", "--force", "--init-binary", binariesDir + "/init", "--kernel-version", opts.kernelVersion, "--config", config}
	if opts.modulesDirectory != "" {
		generatorArgs = append(generatorArgs, "--modules-dir", opts.modulesDirectory)
	}
	generatorArgs = append(generatorArgs, output)
	cmd := exec.Command(binariesDir+"/generator", generatorArgs...)
	if testing.Verbose() {
		log.Print("Create booster.img with " + cmd.String())
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Cannot generate booster.img: %v", err)
	}

	// check generated image integrity
	var verifyCmd *exec.Cmd
	switch opts.compression {
	case "none":
		verifyCmd = exec.Command("cpio", "-i", "--only-verify-crc", "--file", output)
	case "zstd", "":
		verifyCmd = exec.Command("zstd", "--test", output)
	case "gzip":
		verifyCmd = exec.Command("gzip", "--test", output)
	case "xz":
		verifyCmd = exec.Command("xz", "--test", output)
	case "lz4":
		verifyCmd = exec.Command("lz4", "--test", output)
	default:
		return "", fmt.Errorf("Unknown compression: %s", opts.compression)
	}
	if testing.Verbose() {
		verifyCmd.Stdout = os.Stdout
		verifyCmd.Stderr = os.Stderr
	}
	if err := verifyCmd.Run(); err != nil {
		return "", fmt.Errorf("unable to verify integrity of the output image %s: %v", output, err)
	}

	return output, nil
}

type NetworkConfig struct {
	Interfaces string `yaml:",omitempty"` // comma-separated list of interfaces to initialize at early-userspace

	Dhcp bool `yaml:",omitempty"`

	IP         string `yaml:",omitempty"` // e.g. 10.0.2.15/24
	Gateway    string `yaml:",omitempty"` // e.g. 10.0.2.255
	DNSServers string `yaml:"dns_servers,omitempty"`
}

type GeneratorConfig struct {
	Network              *NetworkConfig `yaml:",omitempty"`
	Universal            bool           `yaml:",omitempty"`
	Modules              string         `yaml:",omitempty"`
	ModulesForceLoad     string         `yaml:"modules_force_load,omitempty"` // comma separated list of extra modules to load at the boot time
	Compression          string         `yaml:",omitempty"`
	MountTimeout         string         `yaml:"mount_timeout,omitempty"`
	ExtraFiles           string         `yaml:"extra_files,omitempty"`
	StripBinaries        bool           `yaml:"strip,omitempty"` // strip symbols from the binaries, shared libraries and kernel modules
	EnableVirtualConsole bool           `yaml:"vconsole,omitempty"`
	EnableLVM            bool           `yaml:"enable_lvm"`
	EnableMdraid         bool           `yaml:"enable_mdraid"`
	MdraidConfigPath     string         `yaml:"mdraid_config_path"`
}

func generateBoosterConfig(output string, opts Opts) error {
	var conf GeneratorConfig

	if opts.enableNetwork {
		net := &NetworkConfig{}
		conf.Network = net

		if opts.useDhcp {
			net.Dhcp = true
		} else {
			net.IP = "10.0.2.15/24"
		}

		net.Interfaces = opts.activeNetIfaces
	}
	conf.Universal = true
	conf.Compression = opts.compression
	conf.MountTimeout = strconv.Itoa(opts.mountTimeout) + "s"
	conf.ExtraFiles = opts.extraFiles
	conf.StripBinaries = opts.stripBinaries
	conf.EnableVirtualConsole = opts.enableVirtualConsole
	conf.EnableLVM = opts.enableLVM
	conf.EnableMdraid = opts.enableMdraid
	conf.MdraidConfigPath = opts.mdraidConf
	conf.Modules = opts.modules
	conf.ModulesForceLoad = opts.modulesForceLoad

	data, err := yaml.Marshal(&conf)
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, data, 0644); err != nil {
		return err
	}
	return nil
}

type Opts struct {
	params               []string
	compression          string
	modules              string // extra modules to include into image
	modulesForceLoad     string
	enableNetwork        bool
	useDhcp              bool
	activeNetIfaces      string
	kernelVersion        string // kernel version
	kernelPath           string
	modulesDirectory     string
	kernelArgs           []string
	disk                 string
	disks                []vmtest.QemuDisk
	containsESP          bool // specifies whether the disks contain ESP with bootloader/kernel/initramfs
	scriptEnvvars        []string
	mountTimeout         int // in seconds
	extraFiles           string
	stripBinaries        bool
	enableVirtualConsole bool
	enableLVM            bool
	enableMdraid         bool
	mdraidConf           string
}

func buildVmInstance(t *testing.T, opts Opts) (*vmtest.Qemu, error) {
	require.True(t, opts.disk == "" || len(opts.disks) == 0, "Opts.disk and Opts.disks cannot be specified together")

	disks := opts.disks
	if opts.disk != "" {
		disks = append(disks, vmtest.QemuDisk{Path: opts.disk, Format: "raw"})
	}
	for _, d := range disks {
		require.NoError(t, checkAsset(d.Path))
	}

	if opts.kernelVersion == "" {
		if kernel, ok := kernelVersions["linux"]; ok {
			opts.kernelVersion = kernel
		} else {
			require.Fail(t, "System does not have 'linux' package installed needed for the integration tests")
		}
	}

	workDir := t.TempDir()
	initRamfs, err := generateInitRamfs(workDir, opts)
	require.NoError(t, err)

	params := []string{"-m", "8G", "-smp", strconv.Itoa(runtime.NumCPU())}
	if os.Getenv("TEST_DISABLE_KVM") != "1" {
		params = append(params, "-enable-kvm", "-cpu", "host")
	}

	kernelArgs := []string{"booster.log=debug", "printk.devkmsg=on"}
	kernelArgs = append(kernelArgs, opts.kernelArgs...)

	// to enable network dump
	// params = append(params, "-object", "filter-dump,id=f1,netdev=n1,file=network.dat")

	params = append(params, opts.params...)

	// provide host's directory as a guest block device
	// disks = append(disks, vmtest.QemuDisk{Path: fmt.Sprintf("fat:ro:%s,read-only=on", filepath.Join(kernelsDir, opts.kernelVersion)), Format: "raw"})

	vmlinuzPath := opts.kernelPath
	if vmlinuzPath == "" {
		vmlinuzPath = filepath.Join(kernelsDir, opts.kernelVersion, "vmlinuz")
	}

	if opts.containsESP {
		params = append(params, "-bios", "/usr/share/ovmf/x64/OVMF.fd")

		// ESP partition contains initramfs and cannot be statically built
		// we built the image at runtime
		output := workDir + "/espdisk.raw"

		env := []string{
			"OUTPUT=" + output,
			"KERNEL_IMAGE=" + vmlinuzPath,
			"KERNEL_OPTIONS=" + strings.Join(kernelArgs, " "),
			"INITRAMFS_IMAGE=" + initRamfs,
		}
		env = append(env, opts.scriptEnvvars...)
		require.NoError(t, shell("generate_asset_esp.sh", env...))

		disks = append(disks, vmtest.QemuDisk{Path: output, Format: "raw"})
	}

	options := vmtest.QemuOptions{
		Params:          params,
		OperatingSystem: vmtest.OS_LINUX,
		Disks:           disks,
		Verbose:         testing.Verbose(),
		Timeout:         40 * time.Second,
	}

	if !opts.containsESP {
		options.Kernel = vmlinuzPath
		options.InitRamFs = initRamfs
		options.Append = kernelArgs
	}

	return vmtest.NewQemu(&options)
}

func compileBinaries(dir string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	_ = os.Mkdir("assets", 0755)

	if exists := fileExists("assets/init"); !exists {
		if err := exec.Command("gcc", "-static", "-o", "assets/init", "init/init.c").Run(); err != nil {
			return err
		}
	}

	// Build init binary
	if err := os.Chdir("../init"); err != nil {
		return err
	}
	raceFlag := ""
	if israce.Enabled {
		raceFlag = "-race"
	}
	cmd := exec.Command("go", "build", "-o", dir+"/init", "-tags", "test", raceFlag)
	cmd.Env = os.Environ()
	if !israce.Enabled {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	}
	if testing.Verbose() {
		log.Print("Call 'go build' for init")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Cannot build init binary: %v", err)
	}

	// Generate initramfs
	if err := os.Chdir("../generator"); err != nil {
		return err
	}
	cmd = exec.Command("go", "build", "-o", dir+"/generator", "-tags", "test", raceFlag)
	if testing.Verbose() {
		log.Print("Call 'go build' for generator")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Cannot build generator binary: %v", err)
	}

	return os.Chdir(cwd)
}