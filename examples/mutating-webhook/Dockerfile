FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /webhook ./examples/mutating-webhook

FROM gcr.io/distroless/static:nonroot
COPY --from=build /webhook /webhook
ENTRYPOINT ["/webhook"]
