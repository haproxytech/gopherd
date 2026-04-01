FROM alpine:3.21
RUN addgroup -g 1234 testgroup && \
    adduser -D -u 1234 -G testgroup testuser && \
    mkdir -p /test && chmod 777 /test
COPY gopherd /usr/local/bin/gopherd
ENTRYPOINT ["/usr/local/bin/gopherd"]
