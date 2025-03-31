include buildinfo.mk

BRANCH=main

.PHONY: all 
all: check build push quay

.PHONY: check
check:
ifeq ($(IMAGE),)
	$(error "Missing IMAGE") 
endif

.PHONY: compile
compile:
	cd .. && make

.PHONY: build
build: check compile
	cd .. && \
		docker build --build-arg GIT_BRANCH=$(BRANCH) -t $(IMAGE):$(BRANCH) -f docker/Dockerfile .

.PHONY: push
push: check
	docker push $(IMAGE):$(BRANCH)

# TODO: remove remote images ??
.PHONY: untag
untag: check
	docker image rm $(IMAGE):$(BRANCH)

quay: check
ifeq ($(QUAY),)
	$(warning "Missing QUAY")
	@echo "Skip pushing image on QUAY"
	@exit 0
else ifeq ($(QUAY), n)
	@echo "Skip pushing image on QUAY"
else
	@echo "Pushing on QUAY"
	docker tag $(IMAGE):$(BRANCH) $(QUAY):$(BRANCH)
	docker push $(QUAY):$(BRANCH)
endif
