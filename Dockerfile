# Stage 1: Build the Go application
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum to download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o manga-tool ./cmd/manga-tool

# Stage 2: Create a minimal final image
FROM alpine:latest

# Install required packages and build unrar from source
RUN apk add --no-cache wget p7zip unzip build-base && \
    wget https://www.rarlab.com/rar/unrarsrc-7.0.9.tar.gz && \
    tar -xzf unrarsrc-7.0.9.tar.gz && \
    cd unrar && \
    make && \
    install -v -m755 unrar /usr/local/bin && \
    cd .. && \
    rm -rf unrar unrarsrc-7.0.9.tar.gz && \
    apk del build-base && \
    rm -f /bin/wget && \
    ln -s /usr/bin/wget /bin/wget

# Create a non-root user
RUN addgroup -g 1000 manga && \
    adduser -u 1000 -G manga -s /bin/sh -D manga

WORKDIR /opt/manga-tool

# Copy the built binary from the builder stage
COPY --from=builder /app/manga-tool .

# Copy templates, static files, and other necessary files
COPY templates ./templates/
COPY static ./static/

# Change ownership of the application directory
RUN chown -R manga:manga /opt/manga-tool

# Switch to non-root user
USER manga


# Expose the application port
EXPOSE 25000

# Set the entrypoint
ENTRYPOINT ["./manga-tool"]
