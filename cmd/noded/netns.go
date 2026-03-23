//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func iptCmd() string {
	if _, err := exec.LookPath("iptables-legacy"); err == nil {
		return "iptables-legacy"
	}
	return "iptables"
}

// setupNetns creates a network namespace for a Firecracker VM.
// Ported from loophole-firecracker/daemon/firecracker_vm.go setupVMNetns.
//
// Network layout per VM:
//
//	host:   veth-fc-<id>   10.0.<id>.1/24
//	netns:  veth-ns        10.0.<id>.2/24   (+ tap0 172.16.0.1/24 for FC guest)
//	guest:  eth0           172.16.0.2/24    (configured via kernel cmdline)
//
// Traffic: guest → tap0 → netns routing+NAT → veth → host → NAT → internet
func setupNetns(name string, subnetID int) error {
	ipt := iptCmd()
	hostVeth := fmt.Sprintf("veth-fc-%d", subnetID)
	nsVeth := "veth-ns"
	hostIP := fmt.Sprintf("10.0.%d.1/24", subnetID)
	nsIP := fmt.Sprintf("10.0.%d.2/24", subnetID)
	hostGW := fmt.Sprintf("10.0.%d.1", subnetID)

	cmds := [][]string{
		// Create namespace
		{"ip", "netns", "add", name},

		// Create veth pair
		{"ip", "link", "add", hostVeth, "type", "veth", "peer", "name", nsVeth},
		// Move namespace end into the netns
		{"ip", "link", "set", nsVeth, "netns", name},

		// Configure host end
		{"ip", "addr", "add", hostIP, "dev", hostVeth},
		{"ip", "link", "set", hostVeth, "up"},

		// Configure namespace end
		{"ip", "netns", "exec", name, "ip", "addr", "add", nsIP, "dev", nsVeth},
		{"ip", "netns", "exec", name, "ip", "link", "set", nsVeth, "up"},
		{"ip", "netns", "exec", name, "ip", "link", "set", "lo", "up"},
		{"ip", "netns", "exec", name, "ip", "route", "add", "default", "via", hostGW},

		// Create tap device inside namespace for Firecracker
		{"ip", "netns", "exec", name, "ip", "tuntap", "add", "dev", "tap0", "mode", "tap"},
		{"ip", "netns", "exec", name, "ip", "addr", "add", "172.16.0.1/24", "dev", "tap0"},
		{"ip", "netns", "exec", name, "ip", "link", "set", "tap0", "up"},

		// Enable forwarding and NAT inside the namespace (tap0 → veth-ns)
		{"ip", "netns", "exec", name, "sysctl", "-qw", "net.ipv4.ip_forward=1"},
		{"ip", "netns", "exec", name, ipt, "-t", "nat", "-A", "POSTROUTING", "-o", nsVeth, "-j", "MASQUERADE"},
		{"ip", "netns", "exec", name, ipt, "-A", "FORWARD", "-i", "tap0", "-o", nsVeth, "-j", "ACCEPT"},
		{"ip", "netns", "exec", name, ipt, "-A", "FORWARD", "-i", nsVeth, "-o", "tap0", "-j", "ACCEPT"},

		// Enable forwarding and NAT on the host for this subnet
		{"sysctl", "-qw", "net.ipv4.ip_forward=1"},
		{ipt, "-t", "nat", "-A", "POSTROUTING", "-s", fmt.Sprintf("10.0.%d.0/24", subnetID), "!", "-o", hostVeth, "-j", "MASQUERADE"},
	}

	for _, args := range cmds {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			cleanupNetns(name, subnetID)
			return fmt.Errorf("%v: %w (%s)", args, err, strings.TrimSpace(string(out)))
		}
	}

	return nil
}

func cleanupNetns(name string, subnetID int) {
	ipt := iptCmd()
	hostVeth := fmt.Sprintf("veth-fc-%d", subnetID)

	// Deleting the netns removes the veth pair and all iptables rules inside it
	exec.Command("ip", "netns", "delete", name).Run()

	// Clean up host-side NAT rule
	exec.Command(ipt, "-t", "nat", "-D", "POSTROUTING", "-s",
		fmt.Sprintf("10.0.%d.0/24", subnetID), "!", "-o", hostVeth, "-j", "MASQUERADE").Run()
}

func dialInNetns(name, address string) (net.Conn, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return nil, fmt.Errorf("open current netns: %w", err)
	}
	defer origNS.Close()

	targetNS, err := os.Open("/var/run/netns/" + name)
	if err != nil {
		return nil, fmt.Errorf("open target netns %q: %w", name, err)
	}
	defer targetNS.Close()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		return nil, fmt.Errorf("setns %q: %w", name, err)
	}
	defer unix.Setns(int(origNS.Fd()), unix.CLONE_NEWNET)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s in netns %q: %w", address, name, err)
	}
	return conn, nil
}
