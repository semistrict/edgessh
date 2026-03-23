import { DaemonClient } from "./daemon-client";
import {
  FC_BIN,
  FC_URL,
  GUEST_IP,
  KERNEL_PATH,
  KERNEL_URL,
  VCPU_COUNT,
  VM_KEY_PATH,
  balloonConfig,
  buildBootArgs,
  guestMAC,
  guestShutdownCommand,
  loopholeLogPath,
  vmLogPath,
  vmMemMiB,
  vmSocketPath,
} from "./vm-config";

export interface VMInfo {
  name: string;
  cloneName: string;
  subnetId: number;
  fcPid: number;
  loopholePid: number;
  socket: string;
  memMiB: number;
}

/**
 * Orchestrates VM lifecycle by calling low-level daemon primitives.
 * All business logic (paths, config, sequencing) lives here — the daemon
 * is a dumb proxy with zero domain knowledge.
 */
export class VMOrchestrator {
  constructor(
    private client: DaemonClient,
    private storeURL: string
  ) {}

  // ── Boot ──────────────────────────────────────────────────────────────

  async boot(
    vmName: string,
    subnetId: number,
    rootfsVolume: string,
    pubKey: string
  ): Promise<VMInfo> {
    console.log(`[boot] ${vmName}: ensureAssets`);
    await this.ensureAssets();

    console.log(`[boot] ${vmName}: ensureKeyPair`);
    const internalPubKey = await this.ensureKeyPair();
    const mergedPubKey =
      pubKey && pubKey !== internalPubKey
        ? `${internalPubKey}\n${pubKey}`
        : internalPubKey;

    const attachedVolume = rootfsVolume;
    console.log(`[boot] ${vmName}: attach rootfs=${attachedVolume}`);
    const loopholeLog = loopholeLogPath(vmName);
    await this.client.exec(["rm", "-f", loopholeLog]);

    // Attach loophole device (background process)
    console.log(`[boot] ${vmName}: loophole attach volume=${attachedVolume}`);
    const { pid: loopholePid } = await this.client.procStart(
      ["nice", "-n", "-10", "loophole", "device", "attach", this.storeURL, attachedVolume],
      { name: `loophole:${vmName}`, logFile: loopholeLog }
    );

    // Wait for device to appear
    console.log(`[boot] ${vmName}: polling for device`);
    const devicePath = await this.pollForDevice(vmName, attachedVolume, loopholePid, loopholeLog);
    if (!devicePath) {
      const wait = await this.client.procWait(loopholePid, 1);
      console.log(`[boot] ${vmName}: timed out waiting for device; loophole exited=${wait.exited} exitCode=${wait.exit_code}`);
      const tail = await this.client.exec(["sh", "-c", `tail -50 ${loopholeLog} 2>/dev/null || true`]);
      if (tail.stdout.trim()) {
        console.log(`[boot] ${vmName}: loophole log tail:\n${tail.stdout}`);
      }
      try {
        await this.client.procSignal(loopholePid, "KILL");
      } catch (e: any) {
        console.log(`[boot] ${vmName}: procSignal KILL failed: ${e.message}`);
      }
      throw new Error("timed out waiting for loophole device");
    }
    console.log(`[boot] ${vmName}: device ready at ${devicePath}`);

    // Cleanup any stale netns from a previous run, then setup fresh
    console.log(`[boot] ${vmName}: netns setup subnet=${subnetId}`);
    try {
      await this.client.netnsCleanup(vmName, subnetId);
    } catch (e: any) {
      console.log(`[boot] ${vmName}: netnsCleanup (stale) failed: ${e.message}`);
    }
    await this.client.netnsSetup(vmName, subnetId);

    const sock = vmSocketPath(vmName);
    // Remove stale socket
    await this.client.exec(["rm", "-f", sock]);

    // Start Firecracker
    console.log(`[boot] ${vmName}: starting firecracker`);
    const { pid: fcPid } = await this.client.procStart(
      ["ip", "netns", "exec", vmName, FC_BIN, "--api-sock", sock],
      { name: `fc:${vmName}`, logFile: vmLogPath(vmName) }
    );

    // Wait for socket
    console.log(`[boot] ${vmName}: waiting for FC socket`);
    await this.pollForFile(sock);

    // Get memory info and configure VM
    const info = await this.client.getInfo();
    const memMiB = vmMemMiB(info.memory_mib);

    const pubKeyBase64 = btoa(mergedPubKey);
    const bootArgs = buildBootArgs(vmName, pubKeyBase64);

    await this.client.fc(sock, "PUT", "/boot-source", {
      kernel_image_path: KERNEL_PATH,
      boot_args: bootArgs,
    });
    await this.client.fc(sock, "PUT", "/drives/rootfs", {
      drive_id: "rootfs",
      path_on_host: devicePath,
      is_root_device: true,
      is_read_only: false,
      cache_type: "Writeback",
    });
    await this.client.fc(sock, "PUT", "/network-interfaces/eth0", {
      iface_id: "eth0",
      guest_mac: guestMAC(subnetId),
      host_dev_name: "tap0",
    });
    await this.client.fc(sock, "PUT", "/machine-config", {
      vcpu_count: VCPU_COUNT,
      mem_size_mib: memMiB,
    });
    await this.client.fc(sock, "PUT", "/balloon", balloonConfig(memMiB));
    await this.client.fc(sock, "PUT", "/actions", {
      action_type: "InstanceStart",
    });

    return {
      name: vmName,
      cloneName: attachedVolume,
      subnetId,
      fcPid,
      loopholePid,
      socket: sock,
      memMiB,
    };
  }

  // ── Stop ──────────────────────────────────────────────────────────────

  async stop(vm: VMInfo): Promise<void> {
    // All steps are best-effort — PIDs may be stale after container restart.
    try { await this.guestShutdown(vm.name); } catch (e: any) { console.log(`[stop] ${vm.name}: guestShutdown failed: ${e.message}`); }
    try { await this.client.procWait(vm.fcPid, 10_000); } catch (e: any) { console.log(`[stop] ${vm.name}: procWait fc failed: ${e.message}`); }
    try { await this.client.netnsCleanup(vm.name, vm.subnetId); } catch (e: any) { console.log(`[stop] ${vm.name}: netnsCleanup failed: ${e.message}`); }
    try { await this.client.exec(["rm", "-f", vm.socket]); } catch (e: any) { console.log(`[stop] ${vm.name}: rm socket failed: ${e.message}`); }
    try {
      await this.client.exec(["loophole", "device", "flush", this.storeURL, vm.cloneName]);
    } catch (e: any) { console.log(`[stop] ${vm.name}: loophole flush failed: ${e.message}`); }
    try { await this.client.procSignal(vm.loopholePid, "TERM"); } catch (e: any) { console.log(`[stop] ${vm.name}: procSignal loophole failed: ${e.message}`); }
  }

  // ── Destroy ───────────────────────────────────────────────────────────

  async destroy(vm: VMInfo): Promise<void> {
    await this.stop(vm);
    await this.client.exec([
      "loophole",
      "delete",
      "-y",
      this.storeURL,
      vm.cloneName,
    ]);
  }

  // ── Checkpoint ────────────────────────────────────────────────────────

  async checkpoint(vm: VMInfo): Promise<string> {
    await this.client.fc(vm.socket, "PATCH", "/vm", { state: "Paused" });

    let cpId: string;
    try {
      const result = await this.client.exec([
        "loophole",
        "device",
        "checkpoint",
        this.storeURL,
        vm.cloneName,
      ]);
      if (result.exit_code !== 0) {
        throw new Error(`checkpoint failed: ${result.stderr}`);
      }
      cpId = result.stdout.trim();
    } catch (e) {
      // Resume even if checkpoint failed
      await this.client.fc(vm.socket, "PATCH", "/vm", { state: "Resumed" });
      throw e;
    }

    await this.client.fc(vm.socket, "PATCH", "/vm", { state: "Resumed" });
    return cpId;
  }

  // ── Helpers ───────────────────────────────────────────────────────────

  private async ensureAssets(): Promise<void> {
    const check = await this.client.exec(["stat", KERNEL_PATH]);
    if (check.exit_code !== 0) {
      await this.client.exec(["mkdir", "-p", "/var/lib/firecracker"]);
      await this.client.downloadFile(KERNEL_URL, KERNEL_PATH);
    }

    const fcCheck = await this.client.exec(["stat", FC_BIN]);
    if (fcCheck.exit_code !== 0) {
      await this.client.downloadFile(FC_URL, FC_BIN);
      await this.client.exec(["chmod", "+x", FC_BIN]);
    }
  }

  private async ensureKeyPair(): Promise<string> {
    const result = await this.client.exec(["cat", `${VM_KEY_PATH}.pub`]);
    if (result.exit_code === 0 && result.stdout.trim()) {
      return result.stdout.trim();
    }

    await this.client.exec(["mkdir", "-p", "/etc/edgessh"]);
    await this.client.exec([
      "ssh-keygen",
      "-t",
      "ed25519",
      "-f",
      VM_KEY_PATH,
      "-N",
      "",
      "-q",
    ]);

    const pubResult = await this.client.exec(["cat", `${VM_KEY_PATH}.pub`]);
    if (pubResult.exit_code !== 0) {
      throw new Error(`failed to read generated key: ${pubResult.stderr}`);
    }
    return pubResult.stdout.trim();
  }

  private async pollForDevice(
    vmName: string,
    volumeName: string,
    loopholePid: number,
    loopholeLog: string
  ): Promise<string | null> {
    const loopholeRoots = [
      "/root/.loophole",
      "/.loophole",
      `/proc/${loopholePid}/root/root/.loophole`,
      `/proc/${loopholePid}/root/.loophole`,
    ];

    for (let i = 0; i < 50; i++) {
      await sleep(200);
      const cachedPs = await this.client.exec([
        "sh",
        "-c",
        "ps -ef | grep 'loophole cached --dir' | grep -v grep || true",
      ]);
      const volsetID = cachedPs.stdout.match(/\/cache\/([^/\s]+)\/diskcache/)?.[1];
      if (volsetID) {
        const directPath = `/proc/${loopholePid}/root/root/.loophole/fuse/${volsetID}/${volumeName}/file`;
        const stat = await this.client.exec(["stat", directPath]);
        if (stat.exit_code === 0) {
          if (i === 0 || i % 10 === 9) {
            console.log(`[boot] ${vmName}: using direct proc-root FUSE path ${directPath}`);
          }
          return directPath;
        }
      }

      const fuseDevice = await this.client.exec([
        "sh",
        "-c",
        `${loopholeRoots.map((d) => `find "${d}/fuse" -maxdepth 3 -path '*/${volumeName}/file' 2>/dev/null`).join("; ")} | head -1`,
      ]);
      const fusePath = fuseDevice.stdout.trim();
      if (fusePath) {
        const stat = await this.client.exec(["stat", fusePath]);
        if (stat.exit_code === 0) {
          if (i === 0 || i % 10 === 9) {
            console.log(`[boot] ${vmName}: using FUSE device path ${fusePath}`);
          }
          return fusePath;
        }
      }

      const sockets = await this.client.exec([
        "sh",
        "-c",
        `${loopholeRoots.map((d) => `find "${d}" -maxdepth 3 -name '*.sock' 2>/dev/null`).join("; ")} | sort -u`,
      ]);
      const candidates = sockets.stdout.trim().split("\n").filter(Boolean);
      let lastError = "";
      for (const socket of candidates) {
        try {
          const raw = await this.client.uds(socket, "/status");
          const status = JSON.parse(raw);
          if (status.volume === volumeName && status.store_url === this.storeURL) {
            if (status.device) {
              const deviceCandidates = [
                status.device,
                `/proc/${loopholePid}/root${status.device}`,
              ];
              for (const devicePath of deviceCandidates) {
                const stat = await this.client.exec(["stat", devicePath]);
                if (stat.exit_code === 0) return devicePath;
              }
            }
            if (i === 0 || i % 10 === 9) {
              console.log(`[boot] ${vmName}: socket ${socket} matched volume=${volumeName} but no device yet`);
            }
          }
        } catch (e: any) {
          lastError = e.message;
        }
      }
      if (candidates.length === 0 || lastError) {
        if (i === 0 || i % 10 === 9) {
          const wait = await this.client.procWait(loopholePid, 1);
          const diag = await this.client.exec([
            "sh",
            "-c",
            `printf 'attach_pid=%s root_sock_count=%s slash_sock_count=%s' \
              "$(ps -ef | awk '/loophole device attach .*${volumeName}/ && !/awk/ {c++} END {print c+0}')" \
              "$(find /root/.loophole -maxdepth 3 -name '*.sock' 2>/dev/null | wc -l | tr -d ' ')" \
              "$(find /.loophole -maxdepth 3 -name '*.sock' 2>/dev/null | wc -l | tr -d ' ')"`,
          ]);
          console.log(
            `[boot] ${vmName}: device poll ${i + 1}/50 fusePath=${fusePath || "-"} sockets=${candidates.length} ` +
            `loopholeExited=${wait.exited} loopholeExitCode=${wait.exit_code} lastError=${lastError}`
          );
          console.log(`[boot] ${vmName}: cached ps ${JSON.stringify(cachedPs.stdout)}`);
          console.log(`[boot] ${vmName}: daemon diag ${diag.stdout.trim()}`);
          console.log(
            `[boot] ${vmName}: fuse find exit=${fuseDevice.exit_code} stdout=${JSON.stringify(fuseDevice.stdout)} stderr=${JSON.stringify(fuseDevice.stderr)}`
          );
          console.log(
            `[boot] ${vmName}: socket find exit=${sockets.exit_code} stdout=${JSON.stringify(sockets.stdout)} stderr=${JSON.stringify(sockets.stderr)}`
          );
          const tail = await this.client.exec(["sh", "-c", `tail -20 ${loopholeLog} 2>/dev/null || true`]);
          if (tail.stdout.trim()) {
            console.log(`[boot] ${vmName}: loophole log poll ${i + 1}:\n${tail.stdout}`);
          }
        }
      }
    }
    return null;
  }

  private async pollForFile(path: string): Promise<void> {
    for (let i = 0; i < 20; i++) {
      const result = await this.client.exec(["stat", path]);
      if (result.exit_code === 0) return;
      await sleep(100);
    }
  }

  private async guestShutdown(vmName: string): Promise<void> {
    await this.client.exec([
      "ip",
      "netns",
      "exec",
      vmName,
      "ssh",
      "-o",
      "BatchMode=yes",
      "-o",
      "ConnectTimeout=5",
      "-o",
      "StrictHostKeyChecking=no",
      "-o",
      "UserKnownHostsFile=/dev/null",
      "-i",
      VM_KEY_PATH,
      `root@${GUEST_IP}`,
      guestShutdownCommand(),
    ]);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
