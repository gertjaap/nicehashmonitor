FROM golang:alpine as build

RUN apk update && apk upgrade && \
    apk add --no-cache bash git openssh gcc musl-dev
ENV GOROOT=/usr/local/go
RUN go get github.com/gertjaap/nicehashmonitor
RUN rm -rf /usr/local/go/src/github.com/gertjaap/nicehashmonitor
COPY . /usr/local/go/src/github.com/gertjaap/nicehashmonitor
WORKDIR /usr/local/go/src/github.com/gertjaap/nicehashmonitor
RUN go get ./...
RUN go build

FROM alpine
RUN apk add --no-cache ca-certificates
WORKDIR /app
RUN cd /app
COPY --from=build /usr/local/go/src/github.com/gertjaap/nicehashmonitor/nicehashmonitor /app/bin/nicehashmonitor

EXPOSE 8001

CMD ["bin/nicehashmonitor"]