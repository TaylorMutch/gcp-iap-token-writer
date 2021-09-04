
FROM golang:1.16 as build

WORKDIR /app
ADD . /app

RUN CGO_ENABLED=0 go build -o gcptoken

# Now copy it into our base image.
FROM gcr.io/distroless/static
COPY --from=build /app/gcptoken /app/gcptoken
CMD ["/app/gcptoken"]
