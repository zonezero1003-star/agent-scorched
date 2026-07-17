# Scorched Frontend — build + serve
# NOTE: not verified with a real Node toolchain in this project's sandbox
# (no network access to install packages here) — review before relying on
# it. See AUDIT.md. Serves on port 3000 to match docker-compose.yml.

FROM node:20-alpine AS build
WORKDIR /app
COPY package.json ./
RUN npm install
COPY . .
# Vite reads REACT_APP_*/VITE_* env vars at build time for import.meta.env;
# docker-compose passes them as container env vars at runtime, which Vite's
# static build won't see. For a hackathon demo this is fine (rebuild on
# config change); a real deployment should switch to a runtime-config
# pattern (e.g. injecting a small config.js before serving) instead.
RUN npm run build

FROM node:20-alpine
WORKDIR /app
RUN npm install -g serve
COPY --from=build /app/dist ./dist
EXPOSE 3000
CMD ["serve", "-s", "dist", "-l", "3000"]
