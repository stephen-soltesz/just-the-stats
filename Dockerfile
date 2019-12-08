FROM golang:1.12 as builder

WORKDIR /go/src/github.com/stephen-soltesz/just-the-stats
ADD . ./

ENV CGO_ENABLED 0
RUN go get -v github.com/stephen-soltesz/just-the-stats

FROM alpine
RUN apk add --no-cache ca-certificates && \
    update-ca-certificates
WORKDIR /
COPY --from=builder /go/bin/just-the-stats ./
ENTRYPOINT ["/just-the-stats"]
