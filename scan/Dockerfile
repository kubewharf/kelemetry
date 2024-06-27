FROM golang:1.21-alpine3.18 AS prom2json
RUN GO111MODULE=on go install github.com/prometheus/prom2json/cmd/prom2json@v1.3.3

FROM alpine:3

RUN apk add --no-cache curl jq
RUN curl -L -o /usr/local/bin/kubectl \
	"https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
	chmod +x /usr/local/bin/kubectl
COPY --from=prom2json /go/bin/prom2json /usr/local/bin/prom2json

ADD main.sh /usr/local/bin/scan
ENTRYPOINT ["scan"]
