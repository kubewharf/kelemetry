FROM debian

ARG BIN_FILE
ARG TFCONFIG

ADD $BIN_FILE /usr/local/bin/kelemetry

RUN mkdir -p /app/hack
WORKDIR /app
ADD $TFCONFIG hack/tfconfig.yaml

ENTRYPOINT ["kelemetry"]
