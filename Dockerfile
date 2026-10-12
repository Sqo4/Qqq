FROM node:22-alpine
WORKDIR /app
COPY package.json server.js ./
COPY public ./public
ENV DATA_DIR=/data PORT=8080
RUN mkdir -p /data && chown node:node /data
VOLUME /data
EXPOSE 8080
USER node
CMD ["node", "--no-warnings", "server.js"]
