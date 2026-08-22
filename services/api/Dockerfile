FROM node:24-alpine AS deps
WORKDIR /app
COPY services/api/package.json ./
RUN npm install

FROM deps AS build
COPY services/api ./
RUN npm run build

FROM node:24-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY services/api/package.json ./
RUN npm install --omit=dev
COPY --from=build /app/dist ./dist
EXPOSE 8788
CMD ["node", "dist/index.js"]
