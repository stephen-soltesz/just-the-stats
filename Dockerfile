FROM golang:1.12 as builder

WORKDIR /go/src/github.com/docker/pod-exporter
ADD . ./

ENV CGO_ENABLED 0
RUN go get -v github.com/docker/pod-exporter

FROM alpine
RUN apk add --no-cache ca-certificates && \
    update-ca-certificates
WORKDIR /
COPY --from=builder /go/bin/pod-exporter ./
ENTRYPOINT ["/pod-exporter"]
