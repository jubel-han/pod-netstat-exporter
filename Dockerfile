FROM golang:1.17

ARG TARGETPLATFORM
ARG TARGETARCH
ARG TARGETOS

#ENV GO111MODULE=on
WORKDIR /go/src/github.com/wish/pod-netstat-exporter

# Cache dependencies
COPY go.mod .
COPY go.sum .
RUN go mod download -x

COPY . /go/src/github.com/wish/pod-netstat-exporter/

RUN CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=${TARGETOS} go build -o ./pod-netstat-exporter -a -installsuffix cgo .

FROM alpine:3.11
RUN apk --no-cache add ca-certificates
WORKDIR /
COPY --from=0 /go/src/github.com/wish/pod-netstat-exporter/pod-netstat-exporter /pod-netstat-exporter
CMD /pod-netstat-exporter
