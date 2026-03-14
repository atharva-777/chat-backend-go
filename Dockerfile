FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/server

FROM gcr.io/distroless/base-debian12:nonroot

WORKDIR /
COPY --from=build /out/server /server

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
