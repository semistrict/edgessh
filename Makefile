.PHONY: all clean install daemon vminit image build firecracker-assets r2-upload

all: build

dist:
	mkdir -p dist

# Cross-compile the host node daemon for linux/amd64
daemon: dist/edgessh-noded
dist/edgessh-noded: $(shell find internal/daemon -name '*.go') | dist
	GOOS=linux GOARCH=amd64 go build -o dist/edgessh-noded ./internal/daemon/

# Cross-compile the VM init binary for linux/amd64
vminit: dist/edgessh-init
dist/edgessh-init: $(shell find internal/vminit -name '*.go') | dist
	GOOS=linux GOARCH=amd64 go build -o dist/edgessh-init ./internal/vminit/

# Build the Docker image (host node) and export as gzipped tarball
image: embed/edgessh-image.tar.gz
embed/edgessh-image.tar.gz: dist/edgessh-noded Dockerfile
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

# Build rootfs ext4.gz from Docker image + vminit binary
dist/firecracker/rootfs.ext4.gz: embed/edgessh-image.tar.gz dist/edgessh-init | $(FC_DIR)
	docker create --name fc-rootfs-export --platform linux/amd64 edgessh-noded /bin/true
	docker export fc-rootfs-export > $(FC_DIR)/rootfs.tar
	docker rm fc-rootfs-export
	rm -rf $(FC_DIR)/rootfs
	mkdir -p $(FC_DIR)/rootfs
	tar xf $(FC_DIR)/rootfs.tar -C $(FC_DIR)/rootfs
	echo "nameserver 8.8.8.8" > $(FC_DIR)/rootfs/etc/resolv.conf
	cp dist/edgessh-init $(FC_DIR)/rootfs/edgessh-init
	chmod +x $(FC_DIR)/rootfs/edgessh-init
	$(MKE2FS) -t ext4 -d $(FC_DIR)/rootfs -L rootfs $(FC_DIR)/rootfs.ext4 512M
	gzip -f $(FC_DIR)/rootfs.ext4
	rm -rf $(FC_DIR)/rootfs $(FC_DIR)/rootfs.tar

firecracker-assets: dist/firecracker/vmlinux dist/firecracker/firecracker dist/firecracker/rootfs.ext4.gz

# Upload VM assets to R2
r2-upload: firecracker-assets
	pnpm dlx wrangler r2 object put $(R2_BUCKET)/vmlinux --file dist/firecracker/vmlinux --remote
	pnpm dlx wrangler r2 object put $(R2_BUCKET)/rootfs.ext4.gz --file dist/firecracker/rootfs.ext4.gz --remote
	pnpm dlx wrangler r2 object put $(R2_BUCKET)/firecracker --file dist/firecracker/firecracker --remote

clean:
	rm -rf dist
	rm -f embed/edgessh-noded embed/edgessh-image.tar.gz
