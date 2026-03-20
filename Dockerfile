FROM node:22-alpine AS css
WORKDIR /src
RUN npm install -g @tailwindcss/cli
COPY input.css .
COPY views/ views/
COPY components/ components/
RUN tailwindcss -i ./input.css -o ./assets/css/styles.css --minify

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN go install github.com/a-h/templ/cmd/templ@latest
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /src/assets/css/styles.css assets/css/styles.css
RUN templ generate
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /power-panel .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /power-panel /power-panel
COPY deploy/config.example.yaml /etc/power-panel/config.yaml
VOLUME /var/lib/power-panel
EXPOSE 8080
ENTRYPOINT ["/power-panel"]
CMD ["-config", "/etc/power-panel/config.yaml", "-addr", "0.0.0.0:8080"]
