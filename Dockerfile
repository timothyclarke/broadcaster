FROM golang:1.14-alpine AS build

COPY . /go/src/github.com/timothyclarke/http-request-broadcaster
WORKDIR /go/src/github.com/timothyclarke/http-request-broadcaster
ENV CGO_ENABLED 0

RUN set -ex \
  && apk add --no-cache git \
  && mv main.go http-request-broadcaster.go \
  && go get -d ./... \
  && go build http-request-broadcaster.go

FROM alpine:3.12

RUN set -ex \
  && mkdir -p /home/http-request-broadcaster \
  && addgroup -Sg 1000 broadcaster \
  && adduser  -SG broadcaster -u 1000 -h /home/http-request-broadcaster broadcaster \
  && chown broadcaster:broadcaster /home/http-request-broadcaster

USER broadcaster
COPY --from=build /go/src/github.com/timothyclarke/http-request-broadcaster/http-request-broadcaster /home/http-request-broadcaster/http-request-broadcaster
COPY caches.ini /caches.ini
EXPOSE 8088

RUN id broadcaster

CMD ["/home/http-request-broadcaster/http-request-broadcaster"]



