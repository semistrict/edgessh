# edgessh

`edgessh` provisions Cloudflare Containers, boots Firecracker microVMs inside them, and connects to those VMs over SSH.

## Prerequisites

- Go
- Docker
- A `loophole` binary on your `PATH`

Install `loophole` with:

```bash
go install github.com/semistrict/loophole/cmd/loophole@latest
```

## Getting Started

You need a Cloudflare API **master token** to bootstrap `edgessh`.

Use a token with:

- `User > API Tokens > Edit`

Create one at:

- https://dash.cloudflare.com/profile/api-tokens

Then run:

```bash
edgessh setup --token <CLOUDFLARE_MASTER_TOKEN>
edgessh auth login
```

`setup` uses the master token to mint scoped Workers, Containers, and R2 credentials for normal operation.

## Common Commands

Create a VM:

```bash
edgessh create VM_NAME --rootfs ROOTFS_VOLUME
```

SSH into a VM:

```bash
edgessh ssh VM_NAME
edgessh ssh VM_NAME 'uname -a'
```

Copy files:

```bash
edgessh scp VM_NAME:/path/to/file ./local-file
edgessh scp ./local-file VM_NAME:/path/to/file
```

Expose a port:

```bash
edgessh expose VM_NAME PORT
```

Stop or delete a VM:

```bash
edgessh stop VM_NAME
edgessh delete VM_NAME
```

List VMs and containers:

```bash
edgessh list
edgessh container list
```

## Notes

- `edgessh loophole ...` fetches the loophole store URL and R2 credentials from the authenticated Worker at runtime.
- The default rootfs creation path currently expects repo-local build artifacts such as `dist/edgessh-init` and `dist/edgessh-poweroff`.

## TODO

- Remove the need for the CLI to receive direct R2 credentials for local `loophole create`.
- Replace that path with presigned URLs or a worker-mediated upload flow so rootfs uploads can stay authenticated without exposing raw bucket credentials to the client.
