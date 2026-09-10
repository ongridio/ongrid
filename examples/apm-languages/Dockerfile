# Linux edge demo images; build from the repository root.
FROM maven:3.9.11-eclipse-temurin-21 AS java-build
WORKDIR /src
COPY examples/apm-languages/java/ ./
RUN --mount=type=cache,target=/root/.m2 mvn -q package dependency:copy-dependencies -DoutputDirectory=/deps
ADD --checksum=sha256:bbf83c151b6400709e2f225bdd07a04f839d9d13b8b93464241333fd25d3e3ba https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v2.31.1/opentelemetry-javaagent.jar /agent.jar

FROM eclipse-temurin:21-jre AS java
WORKDIR /app
COPY --from=java-build /src/target/apm-java-1.0.jar ./rpc-metrics.jar
ENV OTEL_JAVAAGENT_EXTENSIONS=/app/rpc-metrics.jar
COPY --from=java-build /src/target/classes/ ./classes/
COPY --from=java-build /deps/ ./deps/
COPY --from=java-build --chmod=644 /agent.jar ./agent.jar
COPY examples/apm-languages/log-entrypoint.sh ./
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["java", "-Xms32m", "-Xmx128m", "-javaagent:/app/agent.jar", "-cp", "/app/classes:/app/deps/*", "App"]

FROM node:22-bookworm-slim AS node
WORKDIR /app
COPY examples/apm-languages/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev --no-audit --no-fund
COPY examples/apm-languages/app.cjs examples/apm-languages/grpc-metrics.cjs examples/apm-languages/health.proto examples/apm-languages/log-entrypoint.sh ./
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["node", "--require", "@opentelemetry/auto-instrumentations-node/register", "app.cjs"]

FROM python:3.14-slim AS python
WORKDIR /app
COPY examples/apm-languages/requirements.txt ./
RUN --mount=type=cache,target=/root/.cache/pip pip install -r requirements.txt
COPY examples/apm-languages/app.py examples/apm-languages/health.proto examples/apm-languages/traffic.py examples/apm-languages/log-entrypoint.sh ./
RUN python -m grpc_tools.protoc -I . --python_out=. --grpc_python_out=. health.proto
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["opentelemetry-instrument", "python", "app.py"]

FROM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY examples/apm-go/ ./examples/apm-go/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -o /app ./examples/apm-go

FROM debian:bookworm-slim AS go
WORKDIR /app
COPY --from=go-build /app ./app
COPY examples/apm-languages/log-entrypoint.sh ./
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["/app/app"]

FROM mcr.microsoft.com/dotnet/sdk:10.0 AS dotnet-build
WORKDIR /src
COPY examples/apm-languages/dotnet/ ./
RUN dotnet restore --locked-mode && dotnet publish --no-restore -c Release -o /out

FROM mcr.microsoft.com/dotnet/aspnet:10.0 AS dotnet
WORKDIR /app
COPY --from=dotnet-build /out/ ./
COPY examples/apm-languages/log-entrypoint.sh ./
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["dotnet", "App.dll"]

FROM php:8.4-cli-bookworm AS php
RUN apt-get update && apt-get install -y --no-install-recommends git unzip libcurl4-openssl-dev && rm -rf /var/lib/apt/lists/* \
    && docker-php-ext-install curl \
    && pecl install opentelemetry-1.2.1 && docker-php-ext-enable opentelemetry
COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer
WORKDIR /app
COPY examples/apm-languages/php/ ./
RUN composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader
COPY examples/apm-languages/log-entrypoint.sh ./
ENV OTEL_PHP_AUTOLOAD_ENABLED=true
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["php", "app.php"]

FROM rust:1-bookworm AS rust-build
WORKDIR /src
COPY examples/apm-languages/rust/ ./
RUN --mount=type=cache,target=/usr/local/cargo/registry --mount=type=cache,target=/src/target \
    cargo build --release --locked && cp target/release/ongrid-apm-rust-example /app

FROM debian:bookworm-slim AS rust
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libssl3 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=rust-build /app ./app
COPY examples/apm-languages/log-entrypoint.sh ./
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["/app/app"]

FROM debian:bookworm AS cpp-build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake git ca-certificates libcurl4-openssl-dev libprotobuf-dev protobuf-compiler libcpp-httplib-dev nlohmann-json3-dev && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch v1.28.0 https://github.com/open-telemetry/opentelemetry-cpp.git /sdk \
    && git -C /sdk submodule update --init --depth 1 third_party/opentelemetry-proto \
    && cmake -S /sdk -B /sdk/build -DCMAKE_BUILD_TYPE=Release -DWITH_OTLP_HTTP=ON -DWITH_OTLP_GRPC=OFF -DBUILD_TESTING=OFF -DWITH_BENCHMARK=OFF -DWITH_EXAMPLES=OFF \
    && cmake --build /sdk/build -j2 && cmake --install /sdk/build
WORKDIR /src
COPY examples/apm-languages/cpp/ ./
RUN cmake -S . -B build -DCMAKE_BUILD_TYPE=Release && cmake --build build -j2

FROM debian:bookworm-slim AS cpp
RUN apt-get update && apt-get install -y --no-install-recommends libcurl4 libprotobuf32 libcpp-httplib0.11 ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=cpp-build /src/build/app ./app
COPY --from=cpp-build /usr/local/lib/libopentelemetry_proto.so /usr/local/lib/
RUN ldconfig
COPY examples/apm-languages/log-entrypoint.sh ./
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["/app/app"]

FROM ruby:3.4-slim-bookworm AS ruby
RUN apt-get update && apt-get install -y --no-install-recommends build-essential && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY examples/apm-languages/ruby/ ./
RUN bundle config set frozen true && bundle install
COPY examples/apm-languages/log-entrypoint.sh ./
ENV OTEL_METRICS_EXPORTER=none
USER 65532:65532
ENTRYPOINT ["sh", "/app/log-entrypoint.sh"]
CMD ["bundle", "exec", "ruby", "app.rb"]
