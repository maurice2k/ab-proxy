# 1. Base Image
FROM golang:1.22.2-alpine

# 2. Working Directory
WORKDIR /app

# 3. Install Git
# git is useful for go modules that might be fetched directly from repositories
# and also for version control information if your build process uses it.
RUN apk add --no-cache git

# 4. Copy Dependency Files
# Copy go.mod and go.sum first to leverage Docker layer caching for dependencies.
COPY go.mod go.sum ./

# 5. Download Dependencies
RUN go mod download

# 6. Copy Source Code
# Copy all .go files (source and test files)
COPY ./*.go ./

# 7. Run Tests
# The integration tests will build the 'ab-proxy' binary as needed within their test setup.
# Using -v for verbose output.
# A non-zero exit code from `go test` will cause this RUN step to fail,
# which in turn will cause the `docker build` to fail if tests don't pass.
# If this Dockerfile is intended to be run (docker run image_name), 
# then CMD should be used. For a test-during-build scenario, RUN is appropriate.
# For the request "The Docker image built from this Dockerfile should, when run, execute all tests",
# we should use CMD.
CMD ["go", "test", "-v", "./..."]
