DEPLOYED=

.PHONY: all 
all: compile build push tag


.PHONY: compile
compile:
	cd .. && make

.PHONY: build
build:
	docker build -t harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice .

.PHONY: push
push:
	docker push harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:$(date +%Y-%m-%d)
	docker tag harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:$(date +%Y-%m-%d) in harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:latest

.PHONY: tag
tag:
	docker tag harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:$(DEPLOYED) in harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice
