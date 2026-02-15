FROM golang:1.25-alpine AS build
WORKDIR /src
COPY src/ .
RUN CGO_ENABLED=0 go build -o /i2pd-web-exporter .

FROM scratch
COPY --from=build /i2pd-web-exporter /i2pd-web-exporter
ENTRYPOINT ["/i2pd-web-exporter"]
