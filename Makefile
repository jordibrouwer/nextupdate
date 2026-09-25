IMAGE   ?= nextupdate
TAG     ?= dev
VERSION ?= dev
NAME    ?= nextupdate
PORT    ?= 8181

.PHONY: help build run stop restart logs test

help: ## Show this help
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/'

build: ## Build the Docker image (nextupdate:dev)
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

run: build ## Build, then run it on http://localhost:8181
	-@docker rm -f $(NAME) >/dev/null 2>&1
	docker run -d --name $(NAME) --restart unless-stopped \
		-p $(PORT):8099 \
		-e NEXTUPDATE_BASE_URL=http://localhost:$(PORT) \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(CURDIR)/data:/data \
		$(IMAGE):$(TAG)
	@echo "nextupdate is starting on http://localhost:$(PORT)"

stop: ## Stop and remove the container (the data in ./data stays)
	-docker rm -f $(NAME)

restart: stop run ## Rebuild and start again

logs: ## Follow the container log
	docker logs -f $(NAME)

test: ## Run the Go and web unit tests
	go vet ./...
	go test -race -count=1 ./...
	node --test web-test/*.test.mjs
