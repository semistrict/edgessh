FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    git \
    htop \
    jq \
    less \
    man-db \
    openssh-client \
    ripgrep \
    sudo \
    tmux \
    unzip \
    vim \
    wget \
    && rm -rf /var/lib/apt/lists/*

COPY dist/edgessh-noded /usr/local/bin/edgessh-noded
RUN chmod +x /usr/local/bin/edgessh-noded

ENTRYPOINT ["/usr/local/bin/edgessh-noded"]
