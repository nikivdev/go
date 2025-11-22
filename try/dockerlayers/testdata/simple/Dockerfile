# simple Dockerfile to show base image and filesystem layers
ARG GLOBAL_VERSION=3.19
FROM alpine:${GLOBAL_VERSION}
LABEL purpose="test"
RUN echo 'hello layers' > /tmp/msg && cp /tmp/msg /msg
COPY . /src
ENV APP_ENV=prod
CMD ["cat", "/msg"]
