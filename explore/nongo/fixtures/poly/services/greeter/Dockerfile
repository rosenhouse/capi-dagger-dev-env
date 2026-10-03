FROM python:3.12-slim AS base
WORKDIR /app

FROM base AS deps
RUN echo "computed-$((6*7))" && echo "deps-stamp=$(date +%s%N) uname=$(uname -m)" > /deps-stamp

FROM base AS app
ARG TARGETARCH
ARG VERSION=unset
ARG HOOK_REV=none
ENV APP_VERSION=$VERSION BUILT_FOR=$TARGETARCH HOOK_REV=$HOOK_REV
COPY --from=deps /deps-stamp /deps-stamp
COPY . /app
CMD ["python", "-u", "/app/app.py"]
