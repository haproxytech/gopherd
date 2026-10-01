# gopherd release image: scratch-based carrier for the static gopherd binary.
# The only supported use is COPY --from; the image ships nothing else.
# Assembled by GoReleaser from the per-platform build context; never compiled here.
FROM scratch

ARG TARGETPLATFORM
COPY --chmod=0755 ${TARGETPLATFORM}/gopherd /usr/local/sbin/gopherd
