# wadist backend image — builds the management console (Gin JSON API) and the
# seed helper as static binaries on distroless.
#
# Build from the repo root:
#   docker build -t wadist-console:latest .
#
# Default entrypoint is the API server (/console). To run the seed job instead,
# override the command, e.g. in a k8s Job:  command: ["/seed"]

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO is not needed (pure-Go deps); produce trimmed, static binaries.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/console ./cmd/console \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/console /console
COPY --from=build /out/seed /seed
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/console"]
