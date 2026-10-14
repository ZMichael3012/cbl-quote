# Контекст сборки: каталог services/ (нужен для replace на ../cbl-kit и ../cbl-protos).
# Сборка: docker build -f cbl-quote/Dockerfile -t cbl/quote:dev .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY cbl-kit/ /src/cbl-kit/
COPY cbl-protos/ /src/cbl-protos/
COPY cbl-quote/ /src/cbl-quote/
WORKDIR /src/cbl-quote
RUN go mod download
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/quote ./cmd/quote

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/quote /app/quote
COPY --from=build /src/cbl-quote/migrations /app/migrations
EXPOSE 8070 9070 9270
ENTRYPOINT ["/app/quote"]
