# Container image for Currently, built by GoReleaser from the release binary.
#
#   docker run -d --name Currently --restart unless-stopped \
#     --device /dev/hidraw0 -p 3493:3493 -p 9300:9300 -p 1161:1161/udp \
#     -v /etc/currently:/etc/currently ghcr.io/duskglow/currently
#
# A container cannot shut its host down; use it for monitoring, or run a
# Currently secondary (client mode) on each host that must shut down.
FROM gcr.io/distroless/static-debian12
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/currently /usr/bin/currently
COPY ${TARGETPLATFORM}/currently-output-mqtt /usr/lib/currently/plugins/currently-output-mqtt
# Mount a config that listens on all interfaces (for example
# listen.http: ":9300"); without one, Currently serves only inside the
# container.
ENV CURRENTLY_CONFIG=/etc/currently/currently.yaml
EXPOSE 3493 9300 1161/udp
ENTRYPOINT ["/usr/bin/currently"]
CMD ["serve"]
