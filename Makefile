.PHONY: fmt lint test layers check
.NOTPARALLEL: check

fmt:
	go fmt ./...

lint:
	go vet ./...

test:
	go test ./...

layers:
	bash scripts/check/layers.sh

check: fmt lint test layers
