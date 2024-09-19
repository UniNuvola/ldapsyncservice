.PHOY: all

compile:
	cd .. && make

build: $(EXE)
	docker build -t harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice .

push: build
	docker push harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:latest 

all: compile build push
