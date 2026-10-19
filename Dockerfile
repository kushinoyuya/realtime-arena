# Multi-stage build → tiny, non-root, static image.
FROM golang:1.21-alpine AS build
WORKDIR /src
COPY . .
# CGO off → a fully static binary that runs on distroless.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
COPY web /app/web
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server", "-addr", ":8080", "-web", "/app/web"]
