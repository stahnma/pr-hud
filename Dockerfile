FROM golang:1.26.10-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pr-hud .

# static includes CA certificates for talking to api.github.com.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /pr-hud /pr-hud
ENTRYPOINT ["/pr-hud"]
