import { DurableObject } from "cloudflare:workers";

const SLEEP_AFTER_MS = 30 * 60 * 1000; // 30 minutes

export class EdgeSSH extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.container = ctx.container;
    this.lastActivity = Date.now();

    // If already running, start the keep-alive alarm loop
    if (this.container && this.container.running) {
      this.container.monitor().catch(() => {});
      this.ctx.storage.setAlarm(Date.now() + 1000);
    }
  }

  async fetch(request) {
    if (!this.container) {
      return new Response("Container runtime not available", { status: 500 });
    }

    this.lastActivity = Date.now();

    const url = new URL(request.url);
    const action = url.pathname.split("/").pop();

    if (action === "start") {
      if (!this.container.running) {
        this.container.start({ enableInternet: true });
        this.container.monitor().catch(() => {});
        // Start the keep-alive alarm loop
        await this.ctx.storage.setAlarm(Date.now() + 1000);
      }
      this.lastActivity = Date.now();
      return new Response(JSON.stringify({ running: this.container.running }), {
        headers: { "Content-Type": "application/json" },
        status: 202,
      });
    }

    if (action === "status") {
      return new Response(JSON.stringify({ running: this.container.running }), {
        headers: { "Content-Type": "application/json" },
      });
    }

    if (action === "stop") {
      if (this.container.running) {
        this.container.signal(15); // SIGTERM
      }
      return new Response(JSON.stringify({ running: false }), {
        headers: { "Content-Type": "application/json" },
      });
    }

    if (!this.container.running) {
      return new Response("Container not running", { status: 503 });
    }

    // Proxy to the container
    const port = this.container.getTcpPort(8080);
    return port.fetch(request);
  }

  // The alarm loop keeps the DO (and container) alive.
  // It sleeps inside the handler to prevent the DO from going idle.
  // After sleepAfter expires with no activity, it sends SIGTERM.
  async alarm() {
    if (!this.container.running) {
      return;
    }

    const elapsed = Date.now() - this.lastActivity;
    if (elapsed >= SLEEP_AFTER_MS) {
      console.log("Activity timeout reached, stopping container");
      this.container.signal(15); // SIGTERM
      return;
    }

    // Sleep inside the alarm to keep the DO alive
    const remaining = SLEEP_AFTER_MS - elapsed;
    const sleepTime = Math.min(remaining, 3 * 60 * 1000); // max 3 min chunks

    await new Promise((resolve) => setTimeout(resolve, sleepTime));

    // Schedule next alarm to continue the loop
    if (this.container.running) {
      await this.ctx.storage.setAlarm(Date.now());
    }
  }
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const parts = url.pathname.split("/").filter(Boolean);
    const name = parts[0];
    if (!name) {
      return new Response("edgessh worker running", { status: 200 });
    }
    const id = env.EDGESSH.idFromName(name);
    const stub = env.EDGESSH.get(id);
    return stub.fetch(request);
  },
};
