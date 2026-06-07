FROM debian:bookworm-slim

ARG MONERO_VERSION=0.18.3.4

RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates bzip2 && \
    curl -fsSL "https://downloads.getmonero.org/cli/monero-linux-x64-v${MONERO_VERSION}.tar.bz2" \
         -o /tmp/monero.tar.bz2 && \
    tar -xjf /tmp/monero.tar.bz2 -C /tmp && \
    mv "/tmp/monero-x86_64-linux-gnu-v${MONERO_VERSION}/monero-wallet-rpc" /usr/local/bin/ && \
    rm -rf /tmp/monero* && \
    apt-get purge -y curl bzip2 && \
    apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/*

EXPOSE 18082
ENTRYPOINT ["monero-wallet-rpc"]
