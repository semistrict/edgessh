FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    fuse3 \
    bash \
    ca-certificates \
    curl \
    e2fsprogs \
    git \
    htop \
    iputils-ping \
    iproute2 \
    iptables \
    jq \
    less \
    man-db \
    openssh-client \
    openssh-server \
    ripgrep \
    sudo \
    tmux \
    unzip \
    vim \
    wget \
    && rm -rf /var/lib/apt/lists/*

# /etc/profile.d runs for any login shell (sh or bash).
# Set TERM, then exec bash so the user always gets bash.
RUN echo 'export TERM=xterm-256color' > /etc/profile.d/edgessh.sh && \
    echo 'export SHELL=/bin/bash' >> /etc/profile.d/edgessh.sh && \
    echo '[ "$(basename $0)" != "bash" ] && [ -x /bin/bash ] && exec /bin/bash --login' >> /etc/profile.d/edgessh.sh

# Pre-configure sshd for when this image is used as a Firecracker rootfs
# (edgessh-init Go binary is added to the rootfs at build time via Makefile, not here)
RUN mkdir -p /run/sshd && \
    ssh-keygen -A && \
    sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config && \
    echo 'root:root' | chpasswd

COPY dist/loophole /usr/local/bin/loophole
COPY dist/edgessh-noded /usr/local/bin/edgessh-noded
RUN chmod +x /usr/local/bin/edgessh-noded

ENTRYPOINT ["/usr/local/bin/edgessh-noded"]
