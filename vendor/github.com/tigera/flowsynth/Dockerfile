FROM golang:alpine AS builder
ARG version=dirty
RUN apk add --no-cache git

ADD ./go.mod /build/
ADD ./go.sum /build/

WORKDIR /build
RUN CGO_ENABLED=0 go get -u 

ADD ./cmd /build/cmd/
ADD ./pkg /build/pkg/

WORKDIR /build/cmd/flowsynth
RUN CGO_ENABLED=0 go build

FROM alpine

COPY --from=builder /build/cmd/flowsynth/flowsynth /usr/local/bin/flowsynth
COPY --from=builder /build/go.sum /usr/local/share/flowsynth/go.sum
RUN echo $version > /usr/local/share/flowsynth/version.txt
