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

# /etc/profile.d runs for any login shell (sh or bash).
# Set TERM, then exec bash so the user always gets bash.
RUN echo 'export TERM=xterm-256color' > /etc/profile.d/edgessh.sh && \
    echo 'export SHELL=/bin/bash' >> /etc/profile.d/edgessh.sh && \
    echo '[ "$(basename $0)" != "bash" ] && [ -x /bin/bash ] && exec /bin/bash --login' >> /etc/profile.d/edgessh.sh && \
    echo 'export PS1="\u@\h:\w\$ "' > /root/.bashrc && \
    echo 'cd ~' >> /root/.bashrc

COPY dist/edgessh-noded /usr/local/bin/edgessh-noded
RUN chmod +x /usr/local/bin/edgessh-noded

ENTRYPOINT ["/usr/local/bin/edgessh-noded"]
