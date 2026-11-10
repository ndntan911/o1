FROM node:24-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
# Build-time configuration baked into the SPA. Sentry stays disabled when the
# DSN is empty. These come after npm ci so a DSN change does not invalidate the
# dependency-install layer.
ARG VITE_SENTRY_DSN=""
ARG VITE_RELEASE=""
ENV VITE_SENTRY_DSN=${VITE_SENTRY_DSN}
ENV VITE_RELEASE=${VITE_RELEASE}
COPY . .
RUN npm run build

FROM caddy:2-alpine
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /app/dist /srv
EXPOSE 80 443
