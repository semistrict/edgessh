// All VM configuration lives here. Change these values and redeploy the Worker
// — no container redeploy needed.

// ── Paths on the container filesystem ──────────────────────────────────────

export const FC_DIR = "/var/lib/firecracker";
export const FC_BIN = `${FC_DIR}/firecracker`;
export const KERNEL_PATH = `${FC_DIR}/vmlinux`;
export const VM_KEY_PATH = "/etc/edgessh/vm_key";

// ── Asset URLs ─────────────────────────────────────────────────────────────

export const KERNEL_URL =
  "https://pub-87ad5457c58141ecb98d7784edb7d55d.r2.dev/vmlinux";
export const FC_URL =
  "https://pub-87ad5457c58141ecb98d7784edb7d55d.r2.dev/firecracker";

// ── Machine config ─────────────────────────────────────────────────────────

export const VCPU_COUNT = 1;
export const DAEMON_RESERVE_MIB = 256;
export const MIN_VM_MEM_MIB = 128;
export const BALLOON_FLOOR_MIB = 512; // minimum memory left for the guest after balloon inflation

// ── Network ────────────────────────────────────────────────────────────────

export const GUEST_IP = "172.16.0.2";
export const GATEWAY_IP = "172.16.0.1";
export const SUBNET_MASK = "255.255.255.0";

// ── Builders ───────────────────────────────────────────────────────────────

export function vmMemMiB(containerMemMiB: number): number {
  const mem = containerMemMiB - DAEMON_RESERVE_MIB;
  return mem < MIN_VM_MEM_MIB ? MIN_VM_MEM_MIB : mem;
}

export function balloonConfig(memMiB: number) {
  return {
    amount_mib: Math.max(0, memMiB - BALLOON_FLOOR_MIB),
    deflate_on_oom: true,
    stats_polling_interval_s: 1,
  };
}

export function buildBootArgs(
  vmName: string,
  pubKeyBase64: string
): string {
  return [
    "console=ttyS0",
    "reboot=k",
    "panic=1",
    "pci=off",
    "root=/dev/vda",
    "rw",
    "init=/edgessh-init",
    `ip=${GUEST_IP}::${GATEWAY_IP}:${SUBNET_MASK}::eth0:off`,
    `edgessh_name=${vmName}`,
    `edgessh_pubkey=${pubKeyBase64}`,
  ].join(" ");
}

export function guestMAC(subnetId: number): string {
  return `AA:FC:00:00:00:${subnetId.toString(16).padStart(2, "0")}`;
}

export function vmSocketPath(vmName: string): string {
  return `/tmp/${vmName}.sock`;
}

export function vmLogPath(vmName: string): string {
  return `/tmp/${vmName}.log`;
}

export function loopholeLogPath(vmName: string): string {
  return `/tmp/${vmName}-loophole.log`;
}

/** The command to run inside the guest (via SSH) for a clean shutdown. */
export function guestShutdownCommand(): string {
  return [
    "sh",
    "-c",
    `"if [ -w /proc/sysrq-trigger ]; then sync; echo 1 > /proc/sys/kernel/sysrq; echo s > /proc/sysrq-trigger; echo u > /proc/sysrq-trigger; echo b > /proc/sysrq-trigger; fi; if [ -x /edgessh-poweroff ]; then exec /edgessh-poweroff; fi; reboot || /sbin/reboot || busybox reboot"`,
  ].join(" ");
}
