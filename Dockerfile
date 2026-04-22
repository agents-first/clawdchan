# syntax=docker/dockerfile:1

# ------------- build -------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Layer-cache the module download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/clawdchan-relay ./cmd/clawdchan-relay

# ------------- runtime -------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clawdchan-relay /clawdchan-relay
USER nonroot:nonroot
EXPOSE 8787
ENTRYPOINT ["/clawdchan-relay"]
CMD ["-addr", ":8787"]
