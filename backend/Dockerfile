FROM node:22-alpine AS frontend-build
WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend-build /web/dist ./webassets/dist
RUN CGO_ENABLED=0 go build -trimpath -o /out/snappy-finance .

FROM python:3.12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tesseract-ocr tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/snappy-finance ./snappy-finance
COPY backend/schema.sql ./schema.sql
COPY backend/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
COPY scripts/parse_bilt.py scripts/parse_bilt_app.py ./scripts/
ENV STATEMENT_PARSER_SCRIPT=/app/scripts/parse_bilt_app.py
ENV PYTHONDONTWRITEBYTECODE=1
USER 65532:65532
EXPOSE 8080
CMD ["./snappy-finance"]
