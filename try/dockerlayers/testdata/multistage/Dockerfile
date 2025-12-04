# multi-stage Dockerfile to exercise COPY --from and ARG inside stages
FROM alpine:3.19 AS base
RUN touch /base.txt

FROM alpine:3.19 AS builder
COPY --from=base /base.txt /build/base.txt
ARG BUILD_FLAG=1
RUN echo $BUILD_FLAG >> /build/base.txt

FROM scratch
COPY --from=builder /build/base.txt /artifact.txt
ENTRYPOINT ["cat", "/artifact.txt"]
