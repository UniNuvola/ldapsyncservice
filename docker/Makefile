DEPLOYED=

.PHONY: compile
compile:
	cd .. && make

.PHONY: build
build:
	docker build -t harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice .

.PHONY: build
push:
	docker push harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:latest 

.PHONY: build
tag:

.PHONY: all 
all: compile build push
