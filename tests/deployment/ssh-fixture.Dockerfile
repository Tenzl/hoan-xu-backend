# Disposable test server only; never deploy this fixture to EC2.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /usr/sbin/nologin chrome-tunnel \
 && usermod -p '*' chrome-tunnel \
 && mkdir -p /run/sshd /home/chrome-tunnel/.ssh
COPY ssh-fixture.sh /fixture-start.sh
RUN sed -i 's/\r$//' /fixture-start.sh && chmod 755 /fixture-start.sh
ENTRYPOINT ["/fixture-start.sh"]
