import { rewriteDurableObjectPath, schedulerStub } from "./helpers";
import type { WorkerEnv } from "./types";

export const worker = {
  async fetch(request: Request, env: WorkerEnv) {
    const url = new URL(request.url);

    if (url.pathname.startsWith("/api/")) {
      return schedulerStub(env).fetch(request);
    }

    const { name, pathname } = rewriteDurableObjectPath(url.pathname);
    if (!name) {
      return new Response("edgessh worker v3", { status: 200 });
    }

    const id = env.EDGESSH.idFromName(name);
    const stub = env.EDGESSH.get(id);
    const newURL = new URL(request.url);
    newURL.pathname = pathname;
    return stub.fetch(new Request(newURL, request));
  },
};
