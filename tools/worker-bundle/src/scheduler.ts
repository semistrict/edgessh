import { SchedulerStore } from "./scheduler-store";
import { SchedulerVMService } from "./scheduler-vm";
import type { DurableObjectStateLike, WorkerEnv } from "./types";

export class SchedulerService {
  private readonly store: SchedulerStore;
  private readonly vm: SchedulerVMService;

  constructor(
    private readonly ctx: DurableObjectStateLike,
    private readonly env: WorkerEnv
  ) {
    this.store = new SchedulerStore(ctx);
    this.vm = new SchedulerVMService(this.store, env);
  }

  ensureTables() {
    this.store.ensureTables();
  }

  async handleRequest(request: Request, url: URL) {
    this.ensureTables();
    const path = url.pathname.replace("/api/", "");

    if (path === "vm/create" && request.method === "POST") return this.scheduleVMCreate(url);
    if (path === "vm/list") return this.listVMs();
    if (path === "vm/stop" && request.method === "POST") return this.scheduleVMStop(url);
    if (path === "vm/delete" && request.method === "POST") return this.scheduleVMDelete(url);
    if (path === "vm/ssh-info") return this.vmSSHInfo(url);
    if (path === "vm/tcp") return this.proxyTCPToVM(request, url);
    if (path === "vm/checkpoint" && request.method === "POST") return this.proxyToVM(url, "checkpoint");
    if (path === "vm/stats") return this.vmStats(url);
    if (path === "container/list") return this.listContainers();
    if (path === "container/stopped" && request.method === "POST") return this.handleContainerStopped(url);
    if (path === "version") return new Response("v3");
    if (path === "container/delete" && request.method === "POST") {
      const id = url.searchParams.get("id");
      if (!id) return new Response("missing id", { status: 400 });
      this.vm.deleteContainer(id);
      return Response.json({ deleted: id });
    }

    return new Response(`not found: path=${path} method=${request.method}`, { status: 404 });
  }

  async handleContainerStopped(url: URL) {
    const id = this.requiredParam(url, "id");
    if (id instanceof Response) return id;
    this.vm.handleContainerStopped(id);
    return Response.json({ cleared: id });
  }

  async scheduleVMCreate(url: URL) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    const rootfs = this.requiredParam(url, "rootfs");
    if (rootfs instanceof Response) return rootfs;
    const sshPubKey = this.requiredParam(url, "ssh_pubkey");
    if (sshPubKey instanceof Response) return sshPubKey;

    if (this.store.vmExists(name)) {
      return new Response(`VM "${name}" already exists`, { status: 409 });
    }

    this.store.insertVM(name, rootfs, sshPubKey);

    try {
      const result = await this.vm.ensureVMRunning(name, sshPubKey);
      return Response.json({ name, container_id: result.container_id, rootfs });
    } catch (e: any) {
      this.store.deleteVM(name);
      return new Response(e.message, { status: 500 });
    }
  }

  async listVMs() {
    return Response.json(this.store.listVMs());
  }

  async scheduleVMStop(url: URL) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    if (!(await this.vm.stopVM(name))) return new Response(`VM "${name}" not found`, { status: 404 });
    return Response.json({ stopped: name });
  }

  async scheduleVMDelete(url: URL) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    if (!(await this.vm.deleteVM(name))) return new Response(`VM "${name}" not found`, { status: 404 });
    return Response.json({ deleted: name });
  }

  async vmSSHInfo(url: URL) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    const sshPubKey = url.searchParams.get("ssh_pubkey") || "";
    return this.withServiceError(async () => {
      const result = await this.vm.ensureVMRunning(name, sshPubKey);
      return Response.json({ do_name: result.do_name, container_id: result.container_id, rootfs: result.rootfs });
    });
  }

  async proxyTCPToVM(request: Request, url: URL) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    const port = this.requiredParam(url, "port");
    if (port instanceof Response) return port;
    const sshPubKey = url.searchParams.get("ssh_pubkey") || "";
    if ((request.headers.get("Upgrade") || "").toLowerCase() !== "websocket") {
      return new Response("expected websocket upgrade", { status: 426 });
    }

    return this.withServiceError(async () => {
      return this.vm.proxyTCPToVM(name, port, sshPubKey, request);
    });
  }

  async proxyToVM(url: URL, action: string) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    return this.withServiceError(async () => {
      return this.vm.proxyToVM(name, action);
    });
  }

  async vmStats(url: URL) {
    const name = this.requiredParam(url, "name");
    if (name instanceof Response) return name;
    return this.withServiceError(async () => {
      return this.vm.getVMStats(name);
    });
  }

  async listContainers() {
    return Response.json(this.store.listContainers());
  }

  private requiredParam(url: URL, key: string) {
    const value = url.searchParams.get(key);
    return value || new Response(`missing ${key}`, { status: 400 });
  }

  private async withServiceError(fn: () => Promise<Response>) {
    try {
      return await fn();
    } catch (e: any) {
      return new Response(e.message, { status: 503 });
    }
  }
}
