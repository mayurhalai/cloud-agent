FROM sandbox-server

# Install tools and agent
COPY --chmod=744 sandboxes/install /tmp/install
RUN /tmp/install tools pi golang && rm -rf /tmp/install

# Golang directory needs to be added to path
ENV PATH="$PATH:/usr/local/go/bin"

# Set user
USER 1000:1000

# Enter code directory and run agent by default
WORKDIR /home/node

# Set agent binary
ENV AGENT_BIN=pi
