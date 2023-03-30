FROM golang:1.20-alpine AS build

RUN mkdir /src
WORKDIR /src
ADD go.mod go.mod
ADD go.sum go.sum
RUN go mod download

ADD pkg pkg
ADD cmd cmd
ADD main.go main.go
RUN go build -v .

FROM alpine
COPY --from=build /src/kelemetry /usr/local/bin/kelemetry

ENTRYPOINT ["kelemetry"]
