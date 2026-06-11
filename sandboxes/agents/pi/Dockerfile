FROM sandbox-server AS tools

FROM node:26-trixie-slim

# Install basic tools
RUN apt-get update && \
    apt-get install -y \
    curl \
    wget \
    git \
    && \
    rm -rf /var/lib/apt/lists/*

# Install pi agent
RUN npm install -g --ignore-scripts @earendil-works/pi-coding-agent && \
    npm cache clean --force

# Enter code directory and run agent by default
WORKDIR /workspace

# Set agent binary
ENV AGENT_BIN=pi

# Mendatory to enable sandbox communication to orchestrator
COPY --from=tools /app/sandbox-server /bin/sandbox-server
EXPOSE 8888
ENTRYPOINT ["/bin/sandbox-server"]
