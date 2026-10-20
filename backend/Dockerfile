FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/snappy-finance .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/snappy-finance ./snappy-finance
COPY schema.sql ./schema.sql
EXPOSE 8080
CMD ["./snappy-finance"]
