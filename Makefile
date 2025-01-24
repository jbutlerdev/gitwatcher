.PHONY: all clean build

# Build everything
all: clean build

# Clean build artifacts
clean:
	rm -f gitwatcher

# Build backend
build:
	cd cmd/gitwatcher && CGO_ENABLED=0 GOOS=linux go build -o ../../gitwatcher

# Run the application
run: all
	./gitwatcher
