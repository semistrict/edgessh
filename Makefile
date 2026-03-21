.PHONY: all clean daemon image build

all: build

dist:
	mkdir -p dist

# Cross-compile the daemon for linux/amd64
daemon: dist/edgessh-noded
dist/edgessh-noded: $(shell find internal/daemon -name '*.go') | dist
	GOOS=linux GOARCH=amd64 go build -o dist/edgessh-noded ./internal/daemon/

# Build the Docker image and export as gzipped tarball
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

clean:
	rm -rf dist
	rm -f embed/edgessh-noded embed/edgessh-image.tar.gz
