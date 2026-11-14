FROM eceasy/cli-proxy-api:v8.0.69
USER root
COPY bin/acp-bridge /usr/local/bin/acp-bridge
COPY bin/agy /usr/local/bin/agy
ENV HOME=/home/acp WORK_ROOT=/work AGY_BINARY=/usr/local/bin/agy BRIDGE_TOKEN_FILE=/run/acp-token
USER 10001:10001
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/acp-bridge"]
