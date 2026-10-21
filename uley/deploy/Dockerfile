# Coordinator image. Build from the uley/ folder:
#   docker build -f deploy/Dockerfile -t uley-coordinator .
#   docker run -d -p 80:80 -p 443:443 -v uley:/data -e ULEY_DOMAIN=hive.example.org -e ULEY_API_KEYS=... uley-coordinator
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /uley-coordinator ./coordinator/cmd/uley-coordinator

FROM gcr.io/distroless/static-debian12
COPY --from=build /uley-coordinator /uley-coordinator
ENV ULEY_DB=/data/uley.db ULEY_ADDR=0.0.0.0:8080
VOLUME /data
EXPOSE 80 443 8080
ENTRYPOINT ["/uley-coordinator"]
