FROM scratch
COPY upstream-monitor-session-proxy /upstream-monitor-session-proxy
USER 0:0
EXPOSE 18320
ENTRYPOINT ["/upstream-monitor-session-proxy"]
