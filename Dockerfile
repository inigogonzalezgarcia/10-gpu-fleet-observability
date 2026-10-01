# One image with the lab's three Go programs: fleet-sim, alert-sink and fleetreport.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ENV CGO_ENABLED=0
RUN for c in fleet-sim alert-sink fleetreport; do go build -trimpath -ldflags "-s -w" -o /out/$c ./cmd/$c; done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
