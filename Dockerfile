# syntax=docker/dockerfile:1

# -- CSS: build Tailwind output --
FROM node:22-alpine AS css
WORKDIR /src
RUN npm install @tailwindcss/cli tailwindcss
COPY input.css .
COPY views/ views/
COPY components/ components/
RUN npx tailwindcss -i ./input.css -o ./assets/css/styles.css --minify

# -- Build: compile Go binary --
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
RUN go install github.com/a-h/templ/cmd/templ@latest
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=css /src/assets/css/styles.css assets/css/styles.css
RUN templ generate
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /power-panel .

# -- Runtime: minimal image --
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="power-panel" \
      org.opencontainers.image.description="Remote server power management via Redfish/WoL" \
      org.opencontainers.image.source="https://github.com/camjac251/power-panel" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /power-panel /power-panel
VOLUME /var/lib/power-panel
EXPOSE 8080
ENTRYPOINT ["/power-panel"]
CMD ["-config", "/etc/power-panel/config.yaml", "-addr", "0.0.0.0:8080"]
