LOOPHOLE_SRC := $(HOME)/src/loophole-workspace/loophole

.PHONY: all clean install daemon vminit vmpoweroff loophole image build firecracker-assets r2-upload deploy deploy-worker

all: build

dist:
	mkdir -p dist

# Cross-compile the host node daemon for linux/amd64
daemon: dist/edgessh-noded
dist/edgessh-noded: $(shell find cmd/noded -name '*.go') | dist
	GOOS=linux GOARCH=amd64 go build -o dist/edgessh-noded ./cmd/noded/

# Cross-compile the VM init binary for linux/amd64
vminit: dist/edgessh-init
dist/edgessh-init: $(shell find cmd/vminit -name '*.go') | dist
	GOOS=linux GOARCH=amd64 go build -o dist/edgessh-init ./cmd/vminit/

# Cross-compile the guest poweroff helper for linux/amd64
vmpoweroff: dist/edgessh-poweroff
dist/edgessh-poweroff: $(shell find cmd/vmpoweroff -name '*.go') | dist
	GOOS=linux GOARCH=amd64 go build -o dist/edgessh-poweroff ./cmd/vmpoweroff/

# Cross-compile loophole for linux/amd64 (always rebuild to pick up source changes)
loophole: | dist
	cd $(LOOPHOLE_SRC) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(CURDIR)/dist/loophole ./cmd/loophole

# Build the Docker image (host node) and export as gzipped tarball
image: embed/edgessh-image.tar.gz
embed/edgessh-image.tar.gz: dist/edgessh-noded Dockerfile | loophole
	docker build --platform linux/amd64 -t edgessh-noded .
	docker save edgessh-noded | gzip > embed/edgessh-image.tar.gz

# Copy daemon into embed/ for go:embed
embed/edgessh-noded: dist/edgessh-noded
	cp dist/edgessh-noded embed/edgessh-noded

# Build the final CLI with all assets embedded
build: embed/edgessh-noded embed/edgessh-image.tar.gz
	go build -o dist/edgessh ./cmd/edgessh/

# Install to $GOPATH/bin
install: embed/edgessh-noded embed/edgessh-image.tar.gz
	go install ./cmd/edgessh/

# --- Firecracker VM assets ---

FC_DIR := dist/firecracker
MKE2FS := /opt/homebrew/opt/e2fsprogs/sbin/mke2fs
R2_BUCKET := edgessh-public

$(FC_DIR):
	mkdir -p $(FC_DIR)

# Download kernel from Firecracker CI
dist/firecracker/vmlinux: | $(FC_DIR)
	curl -fSL -o $@ https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155

# Download firecracker binary
dist/firecracker/firecracker: | $(FC_DIR)
	curl -fSL https://github.com/firecracker-microvm/firecracker/releases/download/v1.15.0/firecracker-v1.15.0-x86_64.tgz \
		| tar xz -C $(FC_DIR)/
	mv $(FC_DIR)/release-v1.15.0-x86_64/firecracker-v1.15.0-x86_64 $@
	rm -rf $(FC_DIR)/release-v1.15.0-x86_64
	chmod +x $@

# Build rootfs ext4.gz from Docker image + guest helpers
dist/firecracker/rootfs.ext4.gz: embed/edgessh-image.tar.gz dist/edgessh-init dist/edgessh-poweroff | $(FC_DIR)
	# Export container filesystem as tarball
	docker create --name fc-rootfs-export --platform linux/amd64 edgessh-noded /bin/true
	docker export fc-rootfs-export > $(FC_DIR)/rootfs.tar
	docker rm fc-rootfs-export
	# Extract tarball, add extras, build ext4 from directory
	rm -rf $(FC_DIR)/rootfs
	mkdir -p $(FC_DIR)/rootfs
	tar xf $(FC_DIR)/rootfs.tar -C $(FC_DIR)/rootfs
	cp dist/edgessh-init $(FC_DIR)/rootfs/edgessh-init
	chmod +x $(FC_DIR)/rootfs/edgessh-init
	cp dist/edgessh-poweroff $(FC_DIR)/rootfs/edgessh-poweroff
	chmod +x $(FC_DIR)/rootfs/edgessh-poweroff
	echo "nameserver 8.8.8.8" > $(FC_DIR)/rootfs/etc/resolv.conf
	$(MKE2FS) -t ext4 -d $(FC_DIR)/rootfs -L rootfs $(FC_DIR)/rootfs.ext4 2G
	gzip -f $(FC_DIR)/rootfs.ext4
	rm -rf $(FC_DIR)/rootfs $(FC_DIR)/rootfs.tar

firecracker-assets: dist/firecracker/vmlinux dist/firecracker/firecracker dist/firecracker/rootfs.ext4.gz

# Upload VM assets to R2
r2-upload: firecracker-assets
	pnpm dlx wrangler r2 object put $(R2_BUCKET)/vmlinux --file dist/firecracker/vmlinux --remote
	pnpm dlx wrangler r2 object put $(R2_BUCKET)/rootfs.ext4.gz --file dist/firecracker/rootfs.ext4.gz --remote
	pnpm dlx wrangler r2 object put $(R2_BUCKET)/firecracker --file dist/firecracker/firecracker --remote

# Build, install, and deploy everything to Cloudflare
deploy: build
	go install ./cmd/edgessh/
	edgessh setup

# Deploy only the Worker script (no image push, no container restart)
deploy-worker:
	go install ./cmd/edgessh/
	edgessh setup --only worker

clean:
	rm -rf dist
	rm -f embed/edgessh-noded embed/edgessh-image.tar.gz
