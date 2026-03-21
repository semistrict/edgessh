import { DurableObject } from "cloudflare:workers";

export class EdgeSSH extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.container = ctx.container;
    if (this.container && this.container.running) {
      this.container.monitor().catch(() => {});
    }
  }

  async fetch(request) {
    if (!this.container) {
      return new Response("Container runtime not available", { status: 500 });
    }

    const url = new URL(request.url);
    const action = url.pathname.split("/").pop();

    if (action === "start") {
      if (!this.container.running) {
        this.container.start({ enableInternet: true });
        this.container.monitor().catch(() => {});
      }
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

    if (!this.container.running) {
      return new Response("Container not running", { status: 503 });
    }

    // Proxy to the container
    const port = this.container.getTcpPort(8080);
    return port.fetch(request);
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
