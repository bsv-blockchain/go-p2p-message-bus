.PHONY: lint test build clean

lint:
	@echo "Running linters..."
	@go vet ./...
	@gofmt -l -s .
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping"; \
		echo "Install with: brew install golangci-lint"; \
	fi

test:
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out

clean:
	@echo "Cleaning..."
	@rm -f p2p_poc example/example coverage.out
