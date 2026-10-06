FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates chromium xvfb x11vnc novnc websockify openbox \
    fonts-liberation fonts-noto-core tini gosu curl x11-utils socat util-linux \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 chromium \
 && useradd --uid 10001 --gid chromium --create-home chromium \
 && mkdir -p /var/lib/shopee-chrome /opt/chromium \
 && chown chromium:chromium /var/lib/shopee-chrome
COPY scripts/ /opt/chromium/
RUN sed -i 's/\r$//' /opt/chromium/*.sh && chmod 755 /opt/chromium/*.sh
ENV DISPLAY=:99 CHROME_PROFILE=/var/lib/shopee-chrome
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
 CMD /opt/chromium/healthcheck.sh
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/chromium/start.sh"]
