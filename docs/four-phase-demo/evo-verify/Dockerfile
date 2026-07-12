# Minimal image for the webhook capture logger (docker-compose service).
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY webhook_capture.go .
RUN go build -o /webhook-capture webhook_capture.go

FROM alpine:3.20
COPY --from=build /webhook-capture /webhook-capture
EXPOSE 9999
ENTRYPOINT ["/webhook-capture"]
