FROM node:24-alpine AS deps
WORKDIR /app
COPY apps/web/package.json ./
RUN npm install

FROM deps AS build
COPY apps/web ./
RUN npm run build

FROM nginx:1.29-alpine
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
