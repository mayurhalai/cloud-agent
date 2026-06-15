FROM sandbox-server

# Install tools and agent
COPY --chmod=744 sandboxes/install /tmp/install
RUN /tmp/install tools opencode && rm -rf /tmp/install

# Set user
USER 1000:1000

# Enter code directory and run agent by default
WORKDIR /home/node

# Set agent binary
ENV AGENT_BIN=opencode
