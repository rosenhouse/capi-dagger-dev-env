IMG ?= controller:latest
VERSION ?= $(shell git describe --always --dirty)
LDFLAGS := -X github.com/acme/fleet-addons/internal/version.Version=$(VERSION)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o bin/manager ./cmd

.PHONY: docker-build
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: deploy
deploy:
	cd config/manager && kustomize edit set image controller=$(IMG)
	kustomize build config/default | kubectl apply -f -

.PHONY: test-e2e
test-e2e:
	go test ./test/e2e/ -v -ginkgo.v
