FROM node:24-bookworm-slim AS build
WORKDIR /app
COPY package*.json ./
RUN --mount=type=secret,id=build_ca NODE_EXTRA_CA_CERTS=/run/secrets/build_ca npm ci
COPY . .
RUN npm run build
FROM nginx:1.28-bookworm
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
