FROM python:3.12-slim AS base
WORKDIR /app

FROM base AS deps
RUN echo "deps-stamp=$(date +%s%N) uname=$(uname -m)" > /deps-stamp

FROM base AS app
ARG TARGETARCH
ARG VERSION=unset
ENV APP_VERSION=$VERSION BUILT_FOR=$TARGETARCH
COPY --from=deps /deps-stamp /deps-stamp
COPY . /app
CMD ["python", "-u", "/app/app.py"]
