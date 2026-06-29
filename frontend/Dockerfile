# wadist frontend (Next.js) production image — multi-stage, standalone output.
#
# Build from the frontend/ directory:
#   docker build -t wadist-frontend:latest ./frontend
#
# The API base URL is baked at build time. For the same-origin klws.cc topology
# the frontend calls /api/v1 (relative), so requests hit the same host and the
# ingress routes /api -> the Gin backend. Override with --build-arg if needed.

FROM node:20-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
# --legacy-peer-deps: Tremor v3 still declares a React ^18 peer; React 19 is fine.
RUN npm ci --legacy-peer-deps

FROM node:20-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ARG NEXT_PUBLIC_API_BASE_URL=/api/v1
ENV NEXT_PUBLIC_API_BASE_URL=$NEXT_PUBLIC_API_BASE_URL
# Cloudflare Turnstile site key (public; safe to bake in). The secret key lives
# server-side in the backend's env (TURNSTILE_SECRET_KEY), never here.
ARG NEXT_PUBLIC_TURNSTILE_SITE_KEY=0x4AAAAAADsWmJ2HOZwHdwUa
ENV NEXT_PUBLIC_TURNSTILE_SITE_KEY=$NEXT_PUBLIC_TURNSTILE_SITE_KEY
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
RUN addgroup -g 1001 -S nodejs && adduser -S nextjs -u 1001
# Standalone output bundles only the files the server needs.
COPY --from=build /app/public ./public
COPY --from=build --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=build --chown=nextjs:nodejs /app/.next/static ./.next/static
USER nextjs
EXPOSE 3000
CMD ["node", "server.js"]
