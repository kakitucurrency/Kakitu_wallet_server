FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS build

WORKDIR /src
ARG TARGETOS TARGETARCH
RUN --mount=target=. \
    --mount=type=cache,id=go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=go-pkg,target=/go/pkg \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/kakitu-server

FROM alpine

RUN apk add --no-cache ca-certificates

# Copy binary
COPY --from=build /out/kakitu-server /bin

EXPOSE 3000

ADD alerts.json .

CMD ["kakitu-server"]
