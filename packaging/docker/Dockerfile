# Container image for nutng, built by GoReleaser from the release binary.
#
#   docker run -d --name nutng --restart unless-stopped \
#     --device /dev/hidraw0 -p 3493:3493 -p 9300:9300 -p 1161:1161/udp \
#     -v /etc/nutng:/etc/nutng ghcr.io/nut-ng/nutng
#
# A container cannot shut its host down; use it for monitoring, or run a
# nutng secondary (client mode) on each host that must shut down.
FROM gcr.io/distroless/static-debian12
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/nutng /usr/bin/nutng
COPY ${TARGETPLATFORM}/nutng-output-mqtt /usr/lib/nutng/plugins/nutng-output-mqtt
# Mount a config that listens on all interfaces (for example
# listen.http: ":9300"); without one, nutng serves only inside the
# container.
ENV NUTNG_CONFIG=/etc/nutng/nutng.yaml
EXPOSE 3493 9300 1161/udp
ENTRYPOINT ["/usr/bin/nutng"]
CMD ["serve"]
