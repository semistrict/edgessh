import { Container } from "@cloudflare/containers";
import type { DurableObject } from "cloudflare:workers";
import { buildContainerEnvVars } from "./helpers";
import { SchedulerService } from "./scheduler";
import { SCHEDULER_NAME } from "./constants";
import type { DurableObjectStateLike, WorkerEnv } from "./types";

export class EdgeSSH extends Container<any> {
  defaultPort = 8080;
  requiredPorts = [8080];
  sleepAfter = "30m";
  enableInternet = true;
  pingEndpoint = "container/health";

  doState: DurableObjectStateLike;
  env: WorkerEnv;
  lastStartAt: string | null;
  lastStop: { at: string; exitCode: number; reason: string } | null;
  lastError: { at: string; error: string } | null;
  scheduler: SchedulerService;

  constructor(ctx: DurableObject["ctx"], env: WorkerEnv) {
    super(ctx, env);
    this.doState = ctx;
    this.env = env;
    this.lastStartAt = null;
    this.lastStop = null;
    this.lastError = null;
    this.scheduler = new SchedulerService(ctx, env);
    this.envVars = buildContainerEnvVars(env);
  }

  async onStart() {
    this.lastStartAt = new Date().toISOString();
  }

  async onStop(params: { exitCode: number; reason: string }) {
    this.lastStop = {
      at: new Date().toISOString(),
      exitCode: params.exitCode,
      reason: params.reason,
    };
    try {
      const schedulerId = this.env.EDGESSH.idFromName(SCHEDULER_NAME);
      const scheduler = this.env.EDGESSH.get(schedulerId);
      const myId = this.doState.id.toString().substring(0, 8);
      await scheduler.fetch(new Request(`http://internal/api/container/stopped?id=${myId}`, { method: "POST" }));
    } catch (e: any) { console.log(`[onStop] notify scheduler failed: ${e.message}`); }
  }

  onError(error: unknown) {
    this.lastError = {
      at: new Date().toISOString(),
      error: String(error),
    };
    throw error;
  }

  async fetch(request: Request) {
    const url = new URL(request.url);

    if (url.pathname.startsWith("/api/")) {
      return this.scheduler.handleRequest(request, url);
    }

    if (url.pathname === "/start") {
      await this.startAndWaitForPorts();
      return Response.json({ running: this.doState.container?.running ?? false }, { status: 202 });
    }

    if (url.pathname === "/status") {
      return Response.json({
        running: this.doState.container?.running ?? false,
        lastStartAt: this.lastStartAt,
        lastStop: this.lastStop,
        lastError: this.lastError,
        state: await this.getState(),
      });
    }

    if (url.pathname === "/stop") {
      if (this.doState.container?.running) {
        await this.stop();
      }
      return Response.json({ running: false });
    }

    return super.fetch(request);
  }
}
