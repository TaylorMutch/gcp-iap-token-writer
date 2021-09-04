FROM golang:1.16 as build

WORKDIR /app
ADD . /app

RUN CGO_ENABLED=0 go build -o gcp-iap-token-writer

# Now copy it into our base image.
FROM gcr.io/distroless/static
COPY --from=build /app/gcp-iap-token-writer /app/gcp-iap-token-writer
CMD ["/app/gcp-iap-token-writer"]
