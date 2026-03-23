import { DaemonClient } from "./daemon-client";
import { parsePrometheusText } from "./prometheus";
import { SchedulerStore } from "./scheduler-store";
import { VMOrchestrator, type VMInfo } from "./vm-orchestrator";
import type { DurableObjectStubLike, SchedulerResult, WorkerEnv } from "./types";

export class SchedulerVMService {
  constructor(
    private readonly store: SchedulerStore,
    private readonly env: WorkerEnv
  ) {}

  async ensureContainerReady(doName: string) {
    const stub = this.stubForName(doName);

    const startResp = await stub.fetch(new Request("http://internal/start"));
    if (!startResp.ok && startResp.status !== 202) {
      throw new Error(`failed to start container: ${await startResp.text()}`);
    }

    for (let i = 0; i < 30; i++) {
      try {
        const resp = await stub.fetch(new Request("http://internal/health"));
        if (resp.ok) return stub;
      } catch (e: any) { console.log(`[ensureContainerReady] health check ${i}: ${e.message}`); }
      await new Promise((resolve) => setTimeout(resolve, 2000));
    }

    throw new Error("container did not become ready");
  }

  async ensureVMRunning(name: string, requestedPubKey = ""): Promise<SchedulerResult> {
    console.log(`[ensureVMRunning] ${name}: start`);
    const vm = this.store.findVM(name);
    if (!vm) throw new Error(`VM "${name}" not found`);

    const sshPubKey = requestedPubKey || vm.ssh_pubkey || "";
    if (requestedPubKey && requestedPubKey !== vm.ssh_pubkey) {
      this.store.updateVMPubKey(name, requestedPubKey);
    }

    console.log(`[ensureVMRunning] ${name}: container_id=${vm.container_id}`);
    if (vm.container_id) {
      const container = this.store.findContainer(vm.container_id);
      if (container) {
        try {
          console.log(`[ensureVMRunning] ${name}: ensuring container ready do=${container.do_name}`);
          const stub = await this.ensureContainerReady(container.do_name);

          // Check if VM is already running (has vm_info from a previous boot)
          const existingInfo = this.store.getVMInfo(name);
          console.log(`[ensureVMRunning] ${name}: vm_info=${existingInfo ? 'yes' : 'null'}`);
          if (existingInfo) {
            // Verify the FC process is actually tracked by the daemon and alive.
            // After a container restart the daemon has no memory of old PIDs,
            // so procWait will throw "pid not tracked". A different process
            // may have reused the PID number, so kill -0 is not sufficient.
            const daemon = new DaemonClient(stub);
            const vmInfo = JSON.parse(existingInfo) as VMInfo;
            let alive = false;
            try {
              const result = await daemon.procWait(vmInfo.fcPid, 1);
              alive = !result.exited;
            } catch (e: any) { console.log(`[ensureVMRunning] ${name}: procWait check failed: ${e.message}`); }
            if (alive) {
              return {
                do_name: container.do_name,
                container_id: container.id,
                rootfs: String(vm.rootfs),
                stub,
              };
            }
            // Stale vm_info — clear it and re-boot below
            console.log(`[ensureVMRunning] ${name}: stale vm_info (pid ${vmInfo.fcPid} dead), clearing`);
            this.store.clearVMInfo(name);
          }

          // Boot the VM via orchestrator. If the netns already exists
          // (leftover from old daemon), the VM is already running — just
          // record stub info and return.
          try {
            await this.bootVMOnContainer(stub, name, String(vm.rootfs), sshPubKey, container.id);
          } catch (e: any) {
            // If boot fails but container is healthy, the VM may already
            // be running from a previous daemon version. Proceed anyway.
            console.log(`boot failed (may already be running): ${e.message}`);
          }
          return {
            do_name: container.do_name,
            container_id: container.id,
            rootfs: String(vm.rootfs),
            stub,
          };
        } catch (e: any) {
          console.log(`[ensureVMRunning] ${name}: container failed, clearing: ${e.message}`);
          this.store.clearVMContainer(name);
          this.store.clearVMInfo(name);
          this.store.decrementContainerVMCount(vm.container_id);
        }
      }
    }

    const result = await this.assignVMToContainer(name, String(vm.rootfs), sshPubKey);
    return { do_name: result.do_name, container_id: result.container_id, rootfs: String(vm.rootfs), stub: result.stub };
  }

  async assignVMToContainer(name: string, rootfs: string, sshPubKey: string) {
    for (let attempt = 0; attempt < 3; attempt++) {
      let container = this.store.findAvailableContainer();

      if (!container) {
        const doName = `host-${this.store.nextContainerOrdinal()}`;
        const doId = this.env.EDGESSH.idFromName(doName);
        const shortID = doId.toString().substring(0, 8);
        this.store.insertContainer(shortID, doName);
        container = { id: shortID, do_name: doName };
      }

      let stub;
      try {
        stub = await this.ensureContainerReady(container.do_name);
      } catch (e: any) {
        console.log(`Container ${container.do_name} broken: ${e.message}, marking full`);
        this.store.markContainerFull(container.id);
        continue;
      }

      await this.bootVMOnContainer(stub, name, rootfs, sshPubKey, container.id);

      this.store.assignVMToContainer(name, container.id);
      this.store.incrementContainerVMCount(container.id);
      return { do_name: container.do_name, container_id: container.id, stub };
    }

    throw new Error("no healthy container available after 3 attempts");
  }

  private async bootVMOnContainer(
    stub: DurableObjectStubLike,
    name: string,
    rootfs: string,
    sshPubKey: string,
    containerID: string
  ) {
    const daemon = new DaemonClient(stub);
    const orchestrator = new VMOrchestrator(daemon, this.env.LOOPHOLE_STORE_URL || "");
    const subnetId = this.store.allocateSubnetId(containerID);

    const vmInfo = await orchestrator.boot(name, subnetId, rootfs, sshPubKey);
    this.store.setVMInfo(name, JSON.stringify(vmInfo));
  }

  private getVMInfoOrThrow(name: string): VMInfo {
    const raw = this.store.getVMInfo(name);
    if (!raw) throw new Error(`VM "${name}" has no runtime info`);
    return JSON.parse(raw) as VMInfo;
  }

  async stopVM(name: string) {
    const vm = this.store.findVM(name);
    if (!vm) return false;

    if (vm.container_id) {
      const container = this.store.findContainer(vm.container_id);
      if (container) {
        try {
          const stub = await this.ensureContainerReady(container.do_name);
          const vmInfo = this.getVMInfoOrThrow(name);
          const daemon = new DaemonClient(stub);
          const orchestrator = new VMOrchestrator(daemon, this.env.LOOPHOLE_STORE_URL || "");
          await orchestrator.stop(vmInfo);
        } catch (e: any) { console.log(`[stopVM] ${name}: stop failed: ${e.message}`); }
      }
      this.store.clearVMContainer(name);
      this.store.clearVMInfo(name);
      this.store.decrementContainerVMCount(vm.container_id);
    }

    return true;
  }

  async deleteVM(name: string) {
    const vm = this.store.findVM(name);
    if (!vm) return false;

    if (vm.container_id) {
      const container = this.store.findContainer(vm.container_id);
      if (container) {
        try {
          const stub = await this.ensureContainerReady(container.do_name);
          const vmInfo = this.getVMInfoOrThrow(name);
          const daemon = new DaemonClient(stub);
          const orchestrator = new VMOrchestrator(daemon, this.env.LOOPHOLE_STORE_URL || "");
          await orchestrator.destroy(vmInfo);
        } catch (e: any) { console.log(`[deleteVM] ${name}: destroy failed: ${e.message}`); }
      }
      this.store.decrementContainerVMCount(vm.container_id);
    }

    this.store.deleteVM(name);
    return true;
  }

  async proxyTCPToVM(name: string, port: string, sshPubKey: string, request: Request) {
    const { stub } = await this.ensureVMRunning(name, sshPubKey);
    const search = new URLSearchParams({ name, port });
    return stub.fetch(new Request(`http://internal/tcp?${search.toString()}`, request));
  }

  async proxyToVM(name: string, action: string) {
    if (action === "checkpoint") {
      return this.checkpointVM(name);
    }
    throw new Error(`unknown action: ${action}`);
  }

  private async checkpointVM(name: string) {
    const vm = this.store.findVM(name);
    if (!vm || !vm.container_id) throw new Error(`VM "${name}" not running`);

    const container = this.store.findContainer(vm.container_id);
    if (!container) throw new Error(`container for VM "${name}" not found`);

    const stub = await this.ensureContainerReady(container.do_name);
    const vmInfo = this.getVMInfoOrThrow(name);
    const daemon = new DaemonClient(stub);
    const orchestrator = new VMOrchestrator(daemon, this.env.LOOPHOLE_STORE_URL || "");
    const cpId = await orchestrator.checkpoint(vmInfo);
    return new Response(cpId + "\n");
  }

  async getVMStats(name: string): Promise<Response> {
    const vm = this.store.findVM(name);
    if (!vm || !vm.container_id) throw new Error(`VM "${name}" not running`);

    const container = this.store.findContainer(vm.container_id);
    if (!container) throw new Error(`container for VM "${name}" not found`);

    const stub = await this.ensureContainerReady(container.do_name);
    const daemon = new DaemonClient(stub);

    // Find the loophole device socket
    const result = await daemon.exec([
      "sh",
      "-c",
      "for d in /root/.loophole /.loophole; do find \"$d/devices\" -maxdepth 1 -name '*.sock' 2>/dev/null; done | sort -u",
    ]);
    const sockets = result.stdout.trim().split("\n").filter(Boolean);
    if (sockets.length === 0) {
      throw new Error("no loophole device sockets found");
    }

    // Fetch metrics from the first device socket
    const metricsText = await daemon.uds(sockets[0], "/metrics");

    // Parse Prometheus text format into JSON
    const metrics = parsePrometheusText(metricsText);

    return Response.json(metrics);
  }

  handleContainerStopped(containerID: string) {
    this.store.clearVMsForContainer(containerID);
    this.store.setContainerVMCountToZero(containerID);
  }

  deleteContainer(containerID: string) {
    this.store.clearVMsForContainer(containerID);
    this.store.deleteContainer(containerID);
  }

  private stubForName(doName: string): DurableObjectStubLike {
    const doID = this.env.EDGESSH.idFromName(doName);
    return this.env.EDGESSH.get(doID);
  }
}
