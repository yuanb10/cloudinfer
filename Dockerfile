FROM golang:1.24 AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/cloudinfer ./cmd/cloudinfer

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/cloudinfer /cloudinfer

ENTRYPOINT ["/cloudinfer"]
