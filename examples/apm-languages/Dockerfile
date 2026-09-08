# Linux edge demo images; build from the repository root.
FROM maven:3.9.11-eclipse-temurin-21 AS java-build
WORKDIR /src
COPY examples/apm-languages/java/ ./
RUN --mount=type=cache,target=/root/.m2 mvn -q package dependency:copy-dependencies -DoutputDirectory=/deps
ADD --checksum=sha256:bbf83c151b6400709e2f225bdd07a04f839d9d13b8b93464241333fd25d3e3ba https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v2.31.1/opentelemetry-javaagent.jar /agent.jar

FROM eclipse-temurin:21-jre AS java
WORKDIR /app
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
COPY examples/apm-languages/app.cjs examples/apm-languages/health.proto examples/apm-languages/log-entrypoint.sh ./
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
