import type { DurableObjectStubLike } from "./types";

export interface ExecResult {
  stdout: string;
  stderr: string;
  exit_code: number;
}

export interface ProcStartResult {
  pid: number;
}

export interface ProcWaitResult {
  exited: boolean;
  exit_code: number;
}

export interface SystemInfo {
  memory_mib: number;
}

/**
 * Typed HTTP client for the Go daemon proxy running inside the container.
 * All calls are proxied through the Durable Object stub → container → daemon.
 */
export class DaemonClient {
  private machineCookiePath?: string;
  private machineCookieValue?: string;

  constructor(private stub: DurableObjectStubLike) {}

  /** Run a command synchronously, return stdout/stderr/exit_code. */
  async exec(cmd: string[]): Promise<ExecResult> {
    await this.ensureMachineCookie();
    return this.rawPost<ExecResult>("/exec", { cmd });
  }

  /** Start a background process, return its PID. Tracked for SIGTERM cleanup. */
  async procStart(
    cmd: string[],
    opts?: { name?: string; logFile?: string }
  ): Promise<ProcStartResult> {
    await this.ensureMachineCookie();
    return this.rawPost<ProcStartResult>("/proc/start", {
      cmd,
      name: opts?.name,
      log_file: opts?.logFile,
    });
  }

  /** Send a signal to a tracked process. */
  async procSignal(pid: number, signal: string): Promise<void> {
    await this.ensureMachineCookie();
    await this.rawPost("/proc/signal", { pid, signal });
  }

  /** Wait for a tracked process to exit (with timeout). */
  async procWait(pid: number, timeoutMs: number): Promise<ProcWaitResult> {
    await this.ensureMachineCookie();
    return this.rawPost<ProcWaitResult>("/proc/wait", {
      pid,
      timeout_ms: timeoutMs,
    });
  }

  /** Create a network namespace with veth + tap + NAT. */
  async netnsSetup(name: string, subnetId: number): Promise<void> {
    await this.ensureMachineCookie();
    await this.rawPost("/netns/setup", { name, subnet_id: subnetId });
  }

  /** Remove a network namespace. */
  async netnsCleanup(name: string, subnetId: number): Promise<void> {
    await this.ensureMachineCookie();
    await this.rawPost("/netns/cleanup", { name, subnet_id: subnetId });
  }

  /** Proxy a request to a Firecracker unix socket. */
  async fc(
    socket: string,
    method: string,
    path: string,
    body: object
  ): Promise<void> {
    await this.ensureMachineCookie();
    await this.rawPost("/fc", { socket, method, path, body });
  }

  /** Get container system info (memory limit etc). */
  async getInfo(): Promise<SystemInfo> {
    await this.ensureMachineCookie();
    const resp = await this.stub.fetch(
      new Request("http://internal/info", { method: "GET" })
    );
    if (!resp.ok) throw new Error(`GET /info: ${await resp.text()}`);
    return resp.json();
  }

  /** Fetch a path from a unix domain socket on the container. */
  async uds(socket: string, path: string): Promise<string> {
    await this.ensureMachineCookie();
    const params = new URLSearchParams({ socket, path });
    const resp = await this.stub.fetch(
      new Request(`http://internal/uds?${params.toString()}`, { method: "GET" })
    );
    if (!resp.ok) throw new Error(`GET /uds: ${await resp.text()}`);
    return resp.text();
  }

  /** Download a file from a URL to a path on the container filesystem. */
  async downloadFile(url: string, destPath: string): Promise<void> {
    const result = await this.exec([
      "wget",
      "-q",
      "-O",
      destPath,
      url,
    ]);
    if (result.exit_code !== 0) {
      throw new Error(
        `download ${url} → ${destPath} failed: ${result.stderr}`
      );
    }
  }

  private async ensureMachineCookie(): Promise<void> {
    if (!this.machineCookiePath || !this.machineCookieValue) {
      const token = crypto.randomUUID().replace(/-/g, "");
      const path = `/tmp/edgessh-machine-cookie-${token}`;
      const value = `edgessh:${token}`;
      const result = await this.rawPost<ExecResult>("/exec", {
        cmd: [
          "sh",
          "-c",
          `printf '%s' '${value}' > '${path}' && cat '${path}'`,
        ],
      });
      if (result.exit_code !== 0 || result.stdout !== value) {
        throw new Error(`failed to initialize daemon machine cookie: ${result.stderr || result.stdout}`);
      }
      this.machineCookiePath = path;
      this.machineCookieValue = value;
      return;
    }

    const result = await this.rawPost<ExecResult>("/exec", {
      cmd: ["cat", this.machineCookiePath],
    });
    if (result.exit_code !== 0 || result.stdout !== this.machineCookieValue) {
      throw new Error(
        `daemon machine cookie mismatch: expected ${this.machineCookieValue}, got ${JSON.stringify(result.stdout)} stderr=${JSON.stringify(result.stderr)}`
      );
    }
  }

  private async rawPost<T = unknown>(path: string, body: object): Promise<T> {
    const resp = await this.stub.fetch(
      new Request(`http://internal${path}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
    );
    if (!resp.ok) {
      throw new Error(`POST ${path}: ${await resp.text()}`);
    }
    return resp.json() as Promise<T>;
  }
}
